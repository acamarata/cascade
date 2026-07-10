//! Purpose: ANN vector store over sqlite-vec (vec0 virtual tables). Production ANN path.
//! Inputs: f32 dense vectors keyed by chunk/doc id.
//! Outputs: a `VectorStore` trait + `SqliteVecStore` impl; KNN by cosine distance.
//! Constraints: compiled only with `features = ["vec"]` (default). One distance metric end to end.
//! SPORT: cascade-db / vector

use rusqlite::Connection;

use crate::error::Result;

/// A nearest-neighbour hit: the stored id and its distance to the query.
#[derive(Debug, Clone, Copy)]
pub struct Neighbour {
    /// The vector's id (chunk/doc rowid).
    pub id: i64,
    /// Distance to the query (smaller = closer). Cosine distance in [0, 2].
    pub distance: f32,
}

/// A dense-vector ANN index.
pub trait VectorStore: Send + Sync {
    /// Insert or replace a vector for `id`.
    fn upsert(&self, id: i64, vector: &[f32]) -> Result<()>;
    /// Delete a vector by id (used by eviction — no orphans left behind).
    fn delete(&self, id: i64) -> Result<()>;
    /// Return the `k` nearest neighbours to `query`.
    fn query(&self, query: &[f32], k: usize) -> Result<Vec<Neighbour>>;
    /// Number of stored vectors.
    fn len(&self) -> Result<u64>;
    /// Whether the store holds no vectors.
    fn is_empty(&self) -> Result<bool> {
        Ok(self.len()? == 0)
    }
}

/// Register the sqlite-vec extension globally. Idempotent; safe to call once at startup.
pub fn register_sqlite_vec() {
    use rusqlite::ffi::sqlite3_auto_extension;
    // SAFETY: mirrors cascade-rag's loader; sqlite_vec::sqlite3_vec_init is a valid
    // SQLite extension entry point with the expected ABI.
    #[allow(clippy::missing_transmute_annotations)]
    unsafe {
        sqlite3_auto_extension(Some(std::mem::transmute(
            sqlite_vec::sqlite3_vec_init as *const (),
        )));
    }
}

/// A vec0-backed vector store with a fixed dimensionality, using cosine distance.
pub struct SqliteVecStore {
    conn: std::sync::Mutex<Connection>,
    table: String,
}

impl SqliteVecStore {
    /// Open a store at `path` with vectors of dimension `dim`. `register_sqlite_vec`
    /// must have been called first (do it once at process start).
    pub fn open(path: impl AsRef<std::path::Path>, dim: usize) -> Result<Self> {
        let conn = crate::conn::open_configured(path)?;
        Self::with_conn(conn, dim, "vectors")
    }

    /// Build a store on an existing connection and table name.
    pub fn with_conn(conn: Connection, dim: usize, table: &str) -> Result<Self> {
        conn.execute_batch(&format!(
            "CREATE VIRTUAL TABLE IF NOT EXISTS {table} USING vec0(
                embedding float[{dim}] distance_metric=cosine
            );"
        ))?;
        Ok(Self {
            conn: std::sync::Mutex::new(conn),
            table: table.to_string(),
        })
    }

    fn vec_to_bytes(v: &[f32]) -> Vec<u8> {
        let mut out = Vec::with_capacity(v.len() * 4);
        for f in v {
            out.extend_from_slice(&f.to_le_bytes());
        }
        out
    }
}

impl VectorStore for SqliteVecStore {
    fn upsert(&self, id: i64, vector: &[f32]) -> Result<()> {
        let conn = self.conn.lock().expect("vector mutex poisoned");
        let bytes = Self::vec_to_bytes(vector);
        conn.execute(
            &format!("DELETE FROM {} WHERE rowid = ?1", self.table),
            [id],
        )?;
        conn.execute(
            &format!(
                "INSERT INTO {}(rowid, embedding) VALUES(?1, ?2)",
                self.table
            ),
            rusqlite::params![id, bytes],
        )?;
        Ok(())
    }

    fn delete(&self, id: i64) -> Result<()> {
        let conn = self.conn.lock().expect("vector mutex poisoned");
        conn.execute(
            &format!("DELETE FROM {} WHERE rowid = ?1", self.table),
            [id],
        )?;
        Ok(())
    }

    fn query(&self, query: &[f32], k: usize) -> Result<Vec<Neighbour>> {
        let conn = self.conn.lock().expect("vector mutex poisoned");
        let bytes = Self::vec_to_bytes(query);
        let mut stmt = conn.prepare(&format!(
            "SELECT rowid, distance FROM {}
             WHERE embedding MATCH ?1 AND k = ?2
             ORDER BY distance",
            self.table
        ))?;
        let rows = stmt.query_map(rusqlite::params![bytes, k as i64], |r| {
            Ok(Neighbour {
                id: r.get(0)?,
                distance: r.get::<_, f64>(1)? as f32,
            })
        })?;
        let mut out = Vec::new();
        for row in rows {
            out.push(row?);
        }
        Ok(out)
    }

    fn len(&self) -> Result<u64> {
        let conn = self.conn.lock().expect("vector mutex poisoned");
        let n: i64 = conn.query_row(&format!("SELECT COUNT(*) FROM {}", self.table), [], |r| {
            r.get(0)
        })?;
        Ok(n as u64)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn knn_returns_closest_first() {
        register_sqlite_vec();
        let store = SqliteVecStore::open(":memory:", 3).unwrap();
        store.upsert(1, &[1.0, 0.0, 0.0]).unwrap();
        store.upsert(2, &[0.0, 1.0, 0.0]).unwrap();
        store.upsert(3, &[0.9, 0.1, 0.0]).unwrap();
        assert_eq!(store.len().unwrap(), 3);

        let hits = store.query(&[1.0, 0.0, 0.0], 2).unwrap();
        assert_eq!(hits.len(), 2);
        assert_eq!(hits[0].id, 1); // exact match is closest

        store.delete(1).unwrap();
        assert_eq!(store.len().unwrap(), 2);
    }
}
