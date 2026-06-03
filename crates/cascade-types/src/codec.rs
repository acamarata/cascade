//! Length-prefixed async frame codec for cascade IPC.
//!
//! This codec encodes and decodes length-prefixed binary frames using
//! 4-byte big-endian u32 length prefixes. It is used by both the daemon
//! IPC server and CLI/widget clients, as well as by the MCP server in E7.

use std::io;
use tokio::io::{AsyncReadExt, AsyncWriteExt};

/// Maximum frame length: 4 MiB.
pub const MAX_FRAME_LEN: usize = 1024 * 1024 * 4;

/// Frame encoding/decoding errors.
#[derive(Debug)]
pub enum CodecError {
    /// I/O error during read or write.
    Io(io::Error),
    /// Frame length exceeded `MAX_FRAME_LEN`.
    FrameTooLarge {
        /// The declared frame length (from the 4-byte prefix).
        len: usize,
        /// The configured maximum frame length.
        max: usize,
    },
}

impl std::fmt::Display for CodecError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            CodecError::Io(e) => write!(f, "I/O error: {}", e),
            CodecError::FrameTooLarge { len, max } => {
                write!(f, "frame too large: {} > {}", len, max)
            }
        }
    }
}

impl std::error::Error for CodecError {}

impl From<io::Error> for CodecError {
    fn from(err: io::Error) -> Self {
        CodecError::Io(err)
    }
}

/// Frame codec for length-prefixed async I/O.
pub struct FrameCodec;

impl FrameCodec {
    /// Read a length-prefixed frame from the given reader.
    ///
    /// The format is:
    /// - 4 bytes: big-endian u32 frame length
    /// - N bytes: frame body
    ///
    /// Returns `Err(CodecError::FrameTooLarge)` if the declared length
    /// exceeds `MAX_FRAME_LEN`. Returns `Err(CodecError::Io)` on read errors.
    pub async fn read_frame<R: AsyncReadExt + Unpin>(
        reader: &mut R,
    ) -> Result<Vec<u8>, CodecError> {
        let mut len_buf = [0u8; 4];
        reader.read_exact(&mut len_buf).await?;
        let len = u32::from_be_bytes(len_buf) as usize;

        if len > MAX_FRAME_LEN {
            return Err(CodecError::FrameTooLarge {
                len,
                max: MAX_FRAME_LEN,
            });
        }

        let mut body = vec![0u8; len];
        reader.read_exact(&mut body).await?;
        Ok(body)
    }

    /// Write a length-prefixed frame to the given writer.
    ///
    /// The format is:
    /// - 4 bytes: big-endian u32 frame length
    /// - N bytes: frame body
    ///
    /// The frame is flushed after writing.
    pub async fn write_frame<W: AsyncWriteExt + Unpin>(
        writer: &mut W,
        body: &[u8],
    ) -> Result<(), CodecError> {
        let len = (body.len() as u32).to_be_bytes();
        writer.write_all(&len).await?;
        writer.write_all(body).await?;
        writer.flush().await?;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn test_roundtrip_10_byte_payload() {
        let (client, server) = tokio::io::duplex(4096);
        let (_, client_write) = tokio::io::split(client);
        let (server_read, _) = tokio::io::split(server);

        let payload = b"0123456789";

        // Write the frame from one side.
        let write_handle = tokio::spawn(async move {
            let mut w = client_write;
            FrameCodec::write_frame(&mut w, payload).await
        });

        // Read the frame from the other side.
        let read_handle = tokio::spawn(async move {
            let mut r = server_read;
            FrameCodec::read_frame(&mut r).await
        });

        let write_result = write_handle.await.expect("write task panicked");
        let read_result = read_handle.await.expect("read task panicked");

        assert!(write_result.is_ok());
        assert_eq!(read_result.unwrap(), payload);
    }

    #[tokio::test]
    async fn test_roundtrip_zero_byte_payload() {
        let (client, server) = tokio::io::duplex(4096);
        let (_, client_write) = tokio::io::split(client);
        let (server_read, _) = tokio::io::split(server);

        let payload = b"";

        let write_handle = tokio::spawn(async move {
            let mut w = client_write;
            FrameCodec::write_frame(&mut w, payload).await
        });

        let read_handle = tokio::spawn(async move {
            let mut r = server_read;
            FrameCodec::read_frame(&mut r).await
        });

        let write_result = write_handle.await.expect("write task panicked");
        let read_result = read_handle.await.expect("read task panicked");

        assert!(write_result.is_ok());
        assert_eq!(read_result.unwrap(), payload);
    }

    #[tokio::test]
    async fn test_frame_too_large() {
        let (client, server) = tokio::io::duplex(4096);
        let (_, mut client_write) = tokio::io::split(client);
        let (mut server_read, _) = tokio::io::split(server);

        // Write a length that exceeds MAX_FRAME_LEN.
        let len = (MAX_FRAME_LEN + 1) as u32;
        let len_bytes = len.to_be_bytes();
        let _ = client_write.write_all(&len_bytes).await;
        let _ = client_write.flush().await;

        // Try to read; should get FrameTooLarge error.
        let result = FrameCodec::read_frame(&mut server_read).await;
        match result {
            Err(CodecError::FrameTooLarge { len: actual, max }) => {
                assert_eq!(actual, MAX_FRAME_LEN + 1);
                assert_eq!(max, MAX_FRAME_LEN);
            }
            other => panic!("expected FrameTooLarge, got {:?}", other),
        }
    }
}
