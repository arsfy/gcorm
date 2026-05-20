// SQLite example schema for GCO ORM.

datasource db {
  provider = "sqlite"
  url      = "file:./dev.db"
}

generator client {
  provider = "gco-go"
  output   = "./gen"
  package  = "db"
}

model Todo {
  id        Int      @id @default(autoincrement())
  title     String
  completed Boolean  @default(false)
  priority  Int      @default(0)
  createdAt DateTime @default(now())
  updatedAt DateTime @updatedAt

  @@index([completed])
  @@index([priority])
}

model Category {
  id   Int    @id @default(autoincrement())
  name String @unique
}
