CREATE TABLE IF NOT EXISTS "users" (
	"id" SERIAL PRIMARY KEY,
	"email" TEXT NOT NULL UNIQUE,
	"balance" DOUBLE PRECISION NOT NULL,
	"avatar" BYTEA
);

CREATE TABLE IF NOT EXISTS "posts" (
	"id" SERIAL PRIMARY KEY,
	"user_id" BIGINT NOT NULL,
	"body" TEXT NOT NULL,
	FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS "idx_posts_user_id" ON "posts" ("user_id");
