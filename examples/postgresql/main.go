// Example usage of GCORM with PostgreSQL.
//
// This file demonstrates the intended usage pattern after running `gco generate`.
// The generated client code provides a Prisma-like experience for database operations.
//
// Usage:
//
//	# Set up your database URL
//	export DATABASE_URL="postgresql://user:password@localhost:5432/mydb"
//
//	# Generate the client code
//	gco generate
//
//	# Run the example
//	go run main.go
package main

import (
	"context"
	"fmt"
	"os"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Println("Set DATABASE_URL environment variable")
		fmt.Println("Example: postgresql://user:password@localhost:5432/mydb")
		os.Exit(1)
	}

	ctx := context.Background()

	// After running `gco generate`, you would use the generated client:
	//
	//   import "your-module/gen/client"
	//   import "your-module/gen/query"
	//
	//   db, err := sql.Open("postgres", dsn)
	//   c := client.New(db)
	//   defer c.Close()
	//
	//   // Create a user
	//   user, err := c.User.Create().
	//       Set(
	//           query.User.Email.Set("alice@example.com"),
	//           query.User.Name.Set("Alice"),
	//       ).
	//       Do(ctx)
	//
	//   // Find users
	//   users, err := c.User.Query().
	//       Where(query.User.Email.Contains("@example.com")).
	//       OrderBy(query.User.CreatedAt.Desc()).
	//       Do(ctx)
	//
	//   // Transaction
	//   err = c.Tx(ctx, func(tx *client.Client) error {
	//       _, err := tx.User.Create().
	//           Set(query.User.Email.Set("bob@example.com")).
	//           Do(ctx)
	//       if err != nil {
	//           return err
	//       }
	//       _, err = tx.Post.Create().
	//           Set(
	//               query.Post.Title.Set("Hello World"),
	//               query.Post.AuthorId.Set(user.ID),
	//           ).
	//           Do(ctx)
	//       return err
	//   })

	_ = ctx
	fmt.Println("PostgreSQL example - run `gco generate` first to generate client code")
}
