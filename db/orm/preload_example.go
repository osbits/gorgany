package orm

import (
	"fmt"
	"log"
)

// Example usage of the preloading functionality

// ExampleUser represents a user entity with relations
type ExampleUser struct {
	BaseEntity
	ID      int             `gorm:"primaryKey"`
	Name    string          `gorm:"column:name"`
	Email   string          `gorm:"column:email"`
	Profile *ExampleProfile `gorm:"foreignKey:UserID"`     // HasOne relation
	Posts   []ExamplePost   `gorm:"foreignKey:UserID"`     // HasMany relation
	Roles   []ExampleRole   `gorm:"many2many:user_roles;"` // Many2Many relation
}

// ExampleProfile represents a user profile
type ExampleProfile struct {
	BaseEntity
	ID     int    `gorm:"primaryKey"`
	UserID int    `gorm:"column:user_id"`
	Bio    string `gorm:"column:bio"`
	Avatar string `gorm:"column:avatar"`
}

// ExamplePost represents a user post
type ExamplePost struct {
	BaseEntity
	ID      int    `gorm:"primaryKey"`
	UserID  int    `gorm:"column:user_id"`
	Title   string `gorm:"column:title"`
	Content string `gorm:"column:content"`
}

// ExampleRole represents a user role
type ExampleRole struct {
	BaseEntity
	ID          int                 `gorm:"primaryKey"`
	Name        string              `gorm:"column:name"`
	Permissions []ExamplePermission `gorm:"foreignKey:RoleID"` // HasMany relation
}

// ExamplePermission represents a role permission
type ExamplePermission struct {
	BaseEntity
	ID       int    `gorm:"primaryKey"`
	RoleID   int    `gorm:"column:role_id"`
	Name     string `gorm:"column:name"`
	Resource string `gorm:"column:resource"`
}

// ExampleUsage demonstrates how to use the preloading functionality
func ExampleUsage() {
	// This is a demonstration function - you would need to initialize your ORM with a database connection

	// Example 1: Basic preloading of relations
	// users, err := userORM.PreloadWith("Profile", "Posts", "Roles")
	// if err != nil {
	//     log.Fatal(err)
	// }

	// Example 2: Preloading with conditions
	// conditions := map[string]interface{}{
	//     "Posts": map[string]interface{}{"status": "published"},
	//     "Roles": map[string]interface{}{"active": true},
	// }
	// users, err := userORM.PreloadWithCondition(conditions)
	// if err != nil {
	//     log.Fatal(err)
	// }

	// Example 3: Using the fluent builder pattern
	// users, err := userORM.Preload().
	//     With("Profile").
	//     With("Posts").
	//     With("Roles").
	//     WithCondition("Posts", map[string]interface{}{"status": "published"}).
	//     All()
	// if err != nil {
	//     log.Fatal(err)
	// }

	// Example 4: Preloading nested relations
	// users, err := userORM.PreloadWith("Roles.Permissions")
	// if err != nil {
	//     log.Fatal(err)
	// }

	// Example 5: Using convenience methods
	// user, err := userORM.PreloadFirst("Profile", "Posts")
	// if err != nil {
	//     log.Fatal(err)
	// }

	fmt.Println("Preloading examples - see comments for usage patterns")
}

// DemonstratePreloading shows how the preloading works in practice
func DemonstratePreloading() {
	// This function shows the different ways to use preloading

	// 1. Simple preloading - load all users with their profiles
	// users, err := userORM.PreloadWith("Profile")

	// 2. Multiple relations - load users with profiles and posts
	// users, err := userORM.PreloadWith("Profile", "Posts")

	// 3. Conditional preloading - only load published posts
	// conditions := map[string]interface{}{
	//     "Posts": map[string]interface{}{"status": "published"},
	// }
	// users, err := userORM.PreloadWithCondition(conditions)

	// 4. Nested preloading - load users with roles and their permissions
	// users, err := userORM.PreloadWith("Roles.Permissions")

	// 5. Fluent builder for complex scenarios
	// users, err := userORM.Preload().
	//     With("Profile").
	//     With("Posts").
	//     With("Roles").
	//     WithCondition("Posts", map[string]interface{}{"status": "published"}).
	//     WithCondition("Roles", map[string]interface{}{"active": true}).
	//     All()

	log.Println("Preloading demonstration - see comments for actual usage")
}

// PerformanceComparison shows the difference between individual loading vs preloading
func PerformanceComparison() {
	// Traditional approach (N+1 problem):
	// 1. Load all users: SELECT * FROM users
	// 2. For each user, load profile: SELECT * FROM profiles WHERE user_id = ?
	// 3. For each user, load posts: SELECT * FROM posts WHERE user_id = ?
	// 4. For each user, load roles: SELECT * FROM roles JOIN user_roles ON ...

	// Preloading approach (efficient):
	// 1. Load all users: SELECT * FROM users
	// 2. Load all profiles: SELECT * FROM profiles WHERE user_id IN (1,2,3,...)
	// 3. Load all posts: SELECT * FROM posts WHERE user_id IN (1,2,3,...)
	// 4. Load all roles: SELECT * FROM roles JOIN user_roles ON ... WHERE user_roles.user_id IN (1,2,3,...)

	// Benefits:
	// - Reduces database round trips from N+1 to 4
	// - More efficient SQL queries using IN clauses
	// - Better memory usage patterns
	// - Easier to implement caching strategies

	log.Println("Performance comparison documented in comments")
}
