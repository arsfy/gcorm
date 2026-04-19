CREATE TABLE "users" (
  "id" INTEGER NOT NULL,
  "email" TEXT NOT NULL UNIQUE,
  "name" TEXT NOT NULL,
  PRIMARY KEY ("id")
);