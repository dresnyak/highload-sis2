// MOCK: stands in for an auth service. In production the token is issued after login.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"highload-sis2/internal/service/auth"
)

func main() {
	user := flag.Int64("user", 0, "user id (required)")
	ttl := flag.Duration("ttl", 24*time.Hour, "token lifetime")
	flag.Parse()

	if *user <= 0 {
		log.Fatal("pass -user <id>")
	}
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		log.Fatal("JWT_SECRET is not set (see .env.example)")
	}
	token, err := auth.Issue([]byte(secret), *user, *ttl)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(token)
}
