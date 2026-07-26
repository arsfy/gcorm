CREATE TABLE `users` (
  `id` INT NOT NULL,
  `email` VARCHAR(255) NOT NULL,
  `name` VARCHAR(255) NOT NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `uq_users_email` UNIQUE (`email`)
);