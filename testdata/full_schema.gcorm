datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
  schema   = "public"
}

generator go {
  provider     = "gco-go"
  output       = "./gen"
  package      = "db"
  emitRuntime  = false
}

model User {
  id        String   @id @default(uuid())
  email     String   @unique
  name      String?
  role      Role     @default(USER)
  profile   Profile?
  posts     Post[]
  createdAt DateTime @default(now())
  updatedAt DateTime @updatedAt

  @@index([email])
  @@map("users")
}

model Profile {
  id     String @id @default(uuid())
  bio    String?
  userId String @unique
  user   User   @relation(fields: [userId], references: [id])

  @@map("profiles")
}

model Post {
  id        String    @id @default(uuid())
  title     String
  content   String?
  published Boolean   @default(false)
  authorId  String
  author    User      @relation(fields: [authorId], references: [id])
  tags      Tag[]
  createdAt DateTime  @default(now())

  @@index([authorId])
  @@index([createdAt])
  @@map("posts")
}

model Tag {
  id    String @id @default(uuid())
  name  String @unique
  posts Post[]

  @@map("tags")
}

enum Role {
  USER
  ADMIN
  MODERATOR
}
