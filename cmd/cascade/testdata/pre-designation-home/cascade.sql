-- Text dump of a cascade.db as the f688c0b layout leaves it: the jobs set (seven tables, schema 5)
-- applied, jobs_* rows present, no outbox tables, no controller binding, no designation.
-- Loaded into <dataDir>/cascade.db by TestUpgradeFromPreDesignationHome; never a binary db.
CREATE TABLE "applied_migrations" (  "id" INTEGER PRIMARY KEY AUTOINCREMENT,  "schema_version" INTEGER NOT NULL,  "checksum" TEXT NOT NULL,  "applied_at" INTEGER NOT NULL,  "set_id" TEXT NOT NULL );
CREATE TABLE "jobs_job" (  "id" TEXT PRIMARY KEY NOT NULL,  "state" TEXT NOT NULL,  "created_at" INTEGER NOT NULL,  "updated_at" INTEGER NOT NULL,  "capabilities" TEXT NOT NULL,  "mutable_scope" TEXT NOT NULL,  "risk_class" TEXT NOT NULL,  "min_task_class" TEXT NOT NULL,  "node_requirements" TEXT NOT NULL,  "timeout_seconds" INTEGER NOT NULL,  "cost_ceiling" REAL NOT NULL,  "priority" INTEGER NOT NULL,  "consecutive_failed_attempts" INTEGER NOT NULL,  "consequence_class" TEXT NOT NULL,  "data_class" TEXT NOT NULL );
CREATE TABLE "jobs_task_dependency" (  "job_id" TEXT NOT NULL,  "depends_on_job_id" TEXT NOT NULL,  PRIMARY KEY ("job_id", "depends_on_job_id"),  FOREIGN KEY ("job_id") REFERENCES "jobs_job" ("id"),  FOREIGN KEY ("depends_on_job_id") REFERENCES "jobs_job" ("id") );
CREATE TABLE "jobs_execution" (  "id" TEXT PRIMARY KEY NOT NULL,  "job_id" TEXT NOT NULL,  "attempt" INTEGER NOT NULL,  "state" TEXT NOT NULL,  "started_at" INTEGER,  "ended_at" INTEGER,  "pgid" INTEGER,  "heartbeat_at" INTEGER,  FOREIGN KEY ("job_id") REFERENCES "jobs_job" ("id") );
CREATE TABLE "jobs_execution_result" (  "execution_id" TEXT PRIMARY KEY NOT NULL,  "output_summary" TEXT NOT NULL,  "error_kind" TEXT,  "error_message" TEXT,  "artifact_refs" TEXT NOT NULL,  FOREIGN KEY ("execution_id") REFERENCES "jobs_execution" ("id") );
CREATE TABLE "jobs_artifact" (  "id" TEXT PRIMARY KEY NOT NULL,  "job_id" TEXT NOT NULL,  "execution_id" TEXT NOT NULL,  "kind" TEXT NOT NULL,  "blob_key" TEXT NOT NULL,  "path" TEXT NOT NULL,  "consequence_class" TEXT NOT NULL,  "data_class" TEXT NOT NULL,  FOREIGN KEY ("job_id") REFERENCES "jobs_job" ("id"),  FOREIGN KEY ("execution_id") REFERENCES "jobs_execution" ("id") );
CREATE TABLE "jobs_resource_lease" (  "repo_id" TEXT NOT NULL,  "scope_glob" TEXT NOT NULL,  "holder" TEXT NOT NULL,  "issued_at" INTEGER NOT NULL,  "ttl_seconds" INTEGER NOT NULL,  "renew_count" INTEGER NOT NULL,  "journal_ref" TEXT NOT NULL,  "epoch" INTEGER NOT NULL,  "state" TEXT NOT NULL,  PRIMARY KEY ("repo_id", "scope_glob") );
CREATE TABLE "jobs_worktree" (  "path" TEXT PRIMARY KEY NOT NULL,  "lease_repo_id" TEXT NOT NULL,  "lease_scope_glob" TEXT NOT NULL,  "repo" TEXT NOT NULL,  "branch" TEXT NOT NULL );
INSERT INTO applied_migrations (id, schema_version, checksum, applied_at, set_id) VALUES (1, 5, '49c57607174dd896c275ee69cb1971d792b92fa80d395063c903e559e0d0b5f4', 1790812800, 'jobs');
INSERT INTO applied_migrations (id, schema_version, checksum, applied_at, set_id) VALUES (2, 5, '433e84230982cb44df03ea85b1555e5ed27fb3fb74a3522a5ec860a5b2217cd9', 1790812800, 'jobs');
INSERT INTO applied_migrations (id, schema_version, checksum, applied_at, set_id) VALUES (3, 5, '84bde6a677f8d143f45d45162ce4c323b7db39b3833428fe726d7bb966483089', 1790812800, 'jobs');
INSERT INTO applied_migrations (id, schema_version, checksum, applied_at, set_id) VALUES (4, 5, '3a2eade9748a21921263d92e49f6f78624269447297130a8331ac870d05a53d5', 1790812800, 'jobs');
INSERT INTO applied_migrations (id, schema_version, checksum, applied_at, set_id) VALUES (5, 5, 'b18e94994a4accb591b74a1d92d5087629855bce158b2e625806cbebe6e46180', 1790812800, 'jobs');
INSERT INTO applied_migrations (id, schema_version, checksum, applied_at, set_id) VALUES (6, 5, 'd9391296db2cb8e265f4875af74a5ca4a0448863b4d4a22674a642398dec2d77', 1790812800, 'jobs');
INSERT INTO applied_migrations (id, schema_version, checksum, applied_at, set_id) VALUES (7, 5, '4bad1b4e4d4c78de49a7faec3350a0866df3db20701714a679a5bb069789f587', 1790812800, 'jobs');
INSERT INTO jobs_job VALUES ('job-pre-1', 'pending', 1790812800, 1790812800, '[]', '[]', 'low', 'small', '{}', 600, 0.5, 1, 0, 'normal', 'internal');
INSERT INTO jobs_job VALUES ('job-pre-2', 'completed', 1790812801, 1790812900, '["read"]', '["src/**"]', 'low', 'small', '{}', 300, 0.25, 2, 1, 'normal', 'internal');
INSERT INTO jobs_job VALUES ('job-pre-3', 'failed', 1790812802, 1790812950, '[]', '[]', 'medium', 'medium', '{}', 900, 1.5, 3, 2, 'normal', 'internal');
INSERT INTO jobs_task_dependency VALUES ('job-pre-1', 'job-pre-2');
INSERT INTO jobs_execution VALUES ('exec-pre-1', 'job-pre-2', 1, 'succeeded', 1790812810, 1790812890, NULL, 1790812880);
INSERT INTO jobs_execution_result VALUES ('exec-pre-1', 'done', NULL, NULL, '[]');
INSERT INTO jobs_resource_lease VALUES ('repo-pre', 'src/**', 'job-pre-2', 1790812810, 600, 0, 'jref-1', 1, 'released');
