package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
	"go.minekube.com/gate/cmd/gate"
)

var redisClient *redis.Client

func main() {
	// Initialize Redis connection
	redisAddr := os.Getenv("REDIS_ADDR") // Redis address from environment variable
	redisPassword := os.Getenv("REDIS_PASSWORD")
	redisDB := 0

	redisClient = redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: redisPassword,
		DB:       redisDB,
	})

	ctx := context.Background()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}

	// Register proxy to Redis
	podName := os.Getenv("POD_NAME")
	labels := os.Getenv("LABELS") // e.g., "hub" or "minigame"

	registerProxy(ctx, podName, labels)

	// Start Gate proxy
	gate.Execute()
}

func registerProxy(ctx context.Context, podName, labels string) {
	key := fmt.Sprintf("proxy:%s", podName)
	value := map[string]interface{}{
		"labels":  labels,
		"address": os.Getenv("POD_IP"),   // Pod IP should be passed as an environment variable
		"port":    os.Getenv("POD_PORT"), // Gate proxy port
	}

	// Set proxy information in Redis
	err := redisClient.HSet(ctx, key, value).Err()
	if err != nil {
		log.Fatalf("Failed to register proxy in Redis: %v", err)
	}

	// Set expiration to auto-remove stale proxies
	err = redisClient.Expire(ctx, key, 30*time.Second).Err()
	if err != nil {
		log.Printf("Failed to set expiration for proxy: %v", err)
	}

	log.Printf("Registered proxy %s with labels %s in Redis", podName, labels)
}
