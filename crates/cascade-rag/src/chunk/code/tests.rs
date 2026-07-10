//! Tests for CodeChunker across all supported languages.

#[cfg(all(test, feature = "code-chunker"))]
mod tests {
    use std::path::PathBuf;

    use super::super::super::{Chunker, ChunkerConfig};
    use super::super::chunker::CodeChunker;

    fn chunker() -> CodeChunker {
        CodeChunker::new(ChunkerConfig {
            max_chunk_chars: 4000,
            min_chunk_chars: 10,
            overlap_chars: 0,
            ..ChunkerConfig::default()
        })
    }

    // ── Rust ──────────────────────────────────────────────────────────────────

    #[test]
    fn rust_three_functions_plus_preamble() {
        let src = r#"use std::fmt;

/// Adds two numbers.
fn add(a: i32, b: i32) -> i32 { a + b }

fn sub(a: i32, b: i32) -> i32 { a - b }

fn mul(a: i32, b: i32) -> i32 { a * b }
"#;
        let path = PathBuf::from("src/math.rs");
        let chunks = Chunker::chunk(&chunker(), &path, src).expect("chunks");

        let fn_chunks: Vec<_> = chunks
            .iter()
            .filter(|c| c.metadata.get("kind").map(|s| s.as_str()) == Some("fn"))
            .collect();
        assert_eq!(
            fn_chunks.len(),
            3,
            "expected 3 fn chunks, got {}",
            fn_chunks.len()
        );

        // Heading path contains function name.
        assert!(
            fn_chunks[0]
                .heading_path
                .as_deref()
                .unwrap_or("")
                .contains("add"),
            "heading_path missing 'add': {:?}",
            fn_chunks[0].heading_path
        );
        assert!(
            fn_chunks[1]
                .heading_path
                .as_deref()
                .unwrap_or("")
                .contains("sub"),
            "heading_path missing 'sub': {:?}",
            fn_chunks[1].heading_path
        );
        assert!(
            fn_chunks[2]
                .heading_path
                .as_deref()
                .unwrap_or("")
                .contains("mul"),
            "heading_path missing 'mul': {:?}",
            fn_chunks[2].heading_path
        );
    }

    #[test]
    fn rust_line_numbers_match() {
        let src = "use std::fmt;\n\nfn foo() {\n    // body\n}\n";
        let path = PathBuf::from("src/lib.rs");
        let chunks = Chunker::chunk(&chunker(), &path, src).expect("chunks");
        let foo = chunks
            .iter()
            .find(|c| c.heading_path.as_deref().unwrap_or("").contains("foo"))
            .expect("fn foo chunk");
        // "fn foo()" is on line 3 (1-based).
        assert_eq!(
            foo.line_start, 3,
            "expected line_start=3, got {}",
            foo.line_start
        );
    }

    #[test]
    fn rust_doc_comment_attached() {
        let src = "/// Adds two numbers.\nfn add(a: i32, b: i32) -> i32 { a + b }\n";
        let path = PathBuf::from("src/lib.rs");
        let chunks = Chunker::chunk(&chunker(), &path, src).expect("chunks");
        let add = chunks
            .iter()
            .find(|c| c.heading_path.as_deref().unwrap_or("").contains("add"))
            .expect("fn add chunk");
        assert!(
            add.text.contains("Adds two numbers"),
            "doc comment not attached: {:?}",
            add.text
        );
    }

    #[test]
    fn rust_impl_block() {
        let src = r#"struct Foo;

impl Foo {
    fn bar(&self) -> i32 { 42 }
    fn baz(&self) -> i32 { 0 }
}
"#;
        let path = PathBuf::from("src/foo.rs");
        let chunks = Chunker::chunk(&chunker(), &path, src).expect("chunks");
        // impl Foo should be one chunk (not oversized).
        let impl_chunk = chunks
            .iter()
            .find(|c| {
                c.metadata.get("kind").map(|s| s.as_str()) == Some("impl")
                    || c.heading_path.as_deref().unwrap_or("").starts_with("impl")
            })
            .expect("impl chunk");
        assert!(
            impl_chunk
                .heading_path
                .as_deref()
                .unwrap_or("")
                .contains("Foo"),
            "impl heading_path missing 'Foo': {:?}",
            impl_chunk.heading_path
        );
    }

    // ── TypeScript ────────────────────────────────────────────────────────────

    #[test]
    fn typescript_class_and_function() {
        let src = r#"import { useState } from 'react';

class Greeter {
    name: string;
    greet() { return `Hello ${this.name}`; }
}

function helper(x: number): number {
    return x * 2;
}
"#;
        let path = PathBuf::from("src/app.ts");
        let chunks = Chunker::chunk(&chunker(), &path, src).expect("chunks");

        // Should have a class chunk and a function chunk.
        let class_chunk = chunks
            .iter()
            .find(|c| {
                c.metadata.get("kind").map(|s| s.as_str()) == Some("class")
                    || c.heading_path.as_deref().unwrap_or("").contains("class")
            })
            .expect("class chunk");
        assert!(
            class_chunk
                .heading_path
                .as_deref()
                .unwrap_or("")
                .contains("Greeter"),
            "class chunk missing 'Greeter': {:?}",
            class_chunk.heading_path
        );

        let fn_chunk = chunks
            .iter()
            .find(|c| c.heading_path.as_deref().unwrap_or("").contains("helper"))
            .expect("function helper chunk");
        assert!(fn_chunk.line_start > 1);
    }

    // ── Python ────────────────────────────────────────────────────────────────

    #[test]
    fn python_class_with_methods() {
        let src = r#"import os

class Dog:
    def __init__(self, name):
        self.name = name

    def bark(self):
        return f"Woof! I am {self.name}"

def standalone(x):
    return x + 1
"#;
        let path = PathBuf::from("pets.py");
        let chunks = Chunker::chunk(&chunker(), &path, src).expect("chunks");

        let class_chunk = chunks
            .iter()
            .find(|c| c.heading_path.as_deref().unwrap_or("").contains("Dog"))
            .expect("class Dog chunk");
        assert!(
            class_chunk
                .heading_path
                .as_deref()
                .unwrap_or("")
                .contains("Dog"),
            "missing 'Dog' in heading_path: {:?}",
            class_chunk.heading_path
        );

        let standalone = chunks
            .iter()
            .find(|c| {
                c.heading_path
                    .as_deref()
                    .unwrap_or("")
                    .contains("standalone")
            })
            .expect("standalone fn chunk");
        assert!(standalone.line_start > 1);
    }

    // ── JavaScript ────────────────────────────────────────────────────────────

    #[test]
    fn javascript_function_chunks() {
        let src = r#"const PI = 3.14;

function square(x) {
    return x * x;
}

function cube(x) {
    return x * x * x;
}
"#;
        let path = PathBuf::from("math.js");
        let chunks = Chunker::chunk(&chunker(), &path, src).expect("chunks");
        let fn_chunks: Vec<_> = chunks
            .iter()
            .filter(|c| {
                c.heading_path.as_deref().unwrap_or("").contains("square")
                    || c.heading_path.as_deref().unwrap_or("").contains("cube")
            })
            .collect();
        assert_eq!(
            fn_chunks.len(),
            2,
            "expected 2 js fn chunks, got {fn_chunks:?}"
        );
    }

    // ── Oversize split ────────────────────────────────────────────────────────

    #[test]
    fn oversized_impl_splits_inner_fns() {
        // Build a large impl block that exceeds max_chunk_chars.
        let mut src = String::from("struct Big;\n\nimpl Big {\n");
        for i in 0..30 {
            src.push_str(&format!("    fn method_{i}(&self) -> i32 {{\n        // lots of padding padding padding padding padding padding\n        {i}\n    }}\n\n"));
        }
        src.push_str("}\n");

        let config = ChunkerConfig {
            max_chunk_chars: 400, // small enough to force split
            min_chunk_chars: 10,
            overlap_chars: 0,
            ..ChunkerConfig::default()
        };
        let chunker = CodeChunker::new(config);
        let path = PathBuf::from("src/big.rs");
        let chunks = Chunker::chunk(&chunker, &path, &src).expect("chunks");

        // Should have multiple chunks instead of one giant impl.
        assert!(
            chunks.len() > 1,
            "expected split chunks, got {} chunks",
            chunks.len()
        );
        // Every fn chunk should have heading_path.
        for c in &chunks {
            assert!(c.heading_path.is_some(), "chunk missing heading_path");
        }
    }

    // ── Unknown extension fallback ────────────────────────────────────────────

    #[test]
    fn unknown_extension_falls_back_to_semantic() {
        let src = "This is some plain text content. It is not code. It has multiple sentences.";
        let path = PathBuf::from("file.xyz");
        let chunks = Chunker::chunk(&chunker(), &path, src).expect("fallback should not error");
        // SemanticChunker should produce at least one chunk for non-empty input.
        assert!(!chunks.is_empty(), "fallback produced no chunks");
    }

    #[test]
    fn unsupported_mime_falls_back() {
        // .yaml is not supported.
        let src = "key: value\nfoo: bar\n";
        let path = PathBuf::from("config.yaml");
        let result = Chunker::chunk(&chunker(), &path, src);
        assert!(result.is_ok(), "fallback should not return Err");
    }
}
