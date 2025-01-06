package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/redis/go-redis/v9"
	"go.minekube.com/gate/cmd/gate"
)

var redisClient *redis.Client

func main() {
	// Initialize Redis connection
	redisAddr := os.Getenv("REDIS_ADDR")         // Redis address from environment variable
	redisPassword := os.Getenv("REDIS_PASSWORD") // Redis password
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

	// Get environment variables
	labels := os.Getenv("LABELS")    // Namespace (e.g., "hub")
	podName := os.Getenv("POD_NAME") // Pod name (e.g., "hub-proxy-<hash>")
	podPort := os.Getenv("POD_PORT") // Proxy port

	if labels == "" || podName == "" || podPort == "" {
		log.Fatal("LABELS, POD_NAME, and POD_PORT environment variables must be set.")
	}

	// Derive service name from the pod name
	serviceName := getServiceNameFromPod(podName)

	// Construct service DNS
	serviceDNS := fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, labels)

	// Register proxy to Redis
	registerProxy(ctx, labels, serviceDNS, podPort)

	// Start Gate proxy
	gate.Execute()
}

func getServiceNameFromPod(podName string) string {
	// Extract service name by removing the pod hash (e.g., "hub-proxy-<hash>" → "hub-proxy")
	parts := strings.Split(podName, "-")
	if len(parts) < 3 {
		log.Fatalf("Pod name format is invalid: %s", podName)
	}
	return strings.Join(parts[:len(parts)-2], "-") // Join everything except the last two parts
}

func registerProxy(ctx context.Context, labels, serviceDNS, podPort string) {
	key := fmt.Sprintf("proxy:%s", labels) // Key based on namespace (e.g., "proxy:hub")
	value := map[string]interface{}{
		"address": serviceDNS,
		"port":    podPort,
	}

	// Set proxy information in Redis as a hash
	err := redisClient.HSet(ctx, key, value).Err()
	if err != nil {
		log.Fatalf("Failed to register proxy in Redis: %v", err)
	}

	log.Printf("Registered proxy %s with address %s:%s in Redis", labels, serviceDNS, podPort)
}
