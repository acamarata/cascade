fn main() {
    for source in [
        "sqlite-vec.c",
        "sqlite-vec-diskann.c",
        "sqlite-vec-ivf-kmeans.c",
        "sqlite-vec-ivf.c",
        "sqlite-vec-rescore.c",
    ] {
        println!("cargo:rerun-if-changed={source}");
    }

    let sqlite_include = std::env::var("DEP_SQLITE3_INCLUDE")
        .expect("libsqlite3-sys must expose its bundled SQLite headers");

    cc::Build::new()
        .file("sqlite-vec.c")
        .include(sqlite_include)
        .define("SQLITE_CORE", None)
        .warnings(false)
        .compile("sqlite_vec0");
}
