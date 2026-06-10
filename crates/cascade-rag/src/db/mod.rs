//! # db
//!
//! SQLite database layer for cascade-rag.
//!
//! Exposes the migration runner (`migrations::run_migrations`) and will house
//! ORM structs and query helpers in subsequent W-02+ tickets.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::db

pub mod migrations;

pub use migrations::run_migrations;
