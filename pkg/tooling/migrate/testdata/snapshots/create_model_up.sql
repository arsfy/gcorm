CREATE TABLE "users" (
  "id" INTEGER NOT NULL,
  "email" TEXT NOT NULL,
  "name" TEXT NOT NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "uq_users_email" UNIQUE ("email")
);