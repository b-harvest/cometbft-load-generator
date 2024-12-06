package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// RPCRequest represents a JSON-RPC request
type RPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
	ID      int         `json:"id"`
}

// generateRandomString generates a random string of the specified size in bytes.
func generateRandomString(size int) string {
	randomBytes := make([]byte, size)
	_, err := rand.Read(randomBytes)
	if err != nil {
		panic(fmt.Sprintf("Failed to generate random string: %v", err))
	}
	return hex.EncodeToString(randomBytes)[:size]
}

// encodeToBase64 encodes a string to Base64.
func encodeToBase64(input string) string {
	return base64.StdEncoding.EncodeToString([]byte(input))
}

func worker(id int, jobs <-chan []RPCRequest, client *http.Client, sentTxCounter *int64, status200 *int64, status500 *int64, wg *sync.WaitGroup) {
	defer wg.Done()

	for batch := range jobs {
		payload, err := json.Marshal(batch)
		if err != nil {
			fmt.Printf("Error marshalling batch: %v\n", err)
			continue
		}

		// Send batch request
		resp, err := client.Post("http://localhost:26660", "application/json", bytes.NewBuffer(payload))
		if err == nil {
			atomic.AddInt64(sentTxCounter, int64(len(batch)))
			if resp.StatusCode == 200 {
				atomic.AddInt64(status200, 1)
			} else if resp.StatusCode == 500 {
				atomic.AddInt64(status500, 1)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}
}

func main() {
	// Define command-line flags
	startTPS := flag.Int("start_tps", 100, "Starting requests per second")
	step := flag.Int("step", 50, "TPS increment step every 10 seconds")
	testDuration := flag.Int("duration", 30, "Total test duration in seconds")
	numWorkers := flag.Int("workers", runtime.NumCPU()*4, "Number of worker goroutines")
	batchSize := flag.Int("batch_size", 10, "Number of requests per batch")
	txSize := flag.Int("tx_size", 100, "Size of the tx_id in bytes")
	flag.Parse()

	fmt.Printf("Starting test: StartTPS=%d, Step=%d, Duration=%d seconds, Workers=%d, TxSize=%d bytes, BatchSize=%d\n",
		*startTPS, *step, *testDuration, *numWorkers, *txSize, *batchSize)

	// Create a custom HTTP client with connection reuse (Keep-Alive)
	transport := &http.Transport{
		MaxIdleConns:        1000,
		MaxIdleConnsPerHost: 100,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   3 * time.Second,
	}

	// Job and status counters
	jobs := make(chan []RPCRequest, (*startTPS+*step*(*testDuration/10))*2)
	var sentTxCounter, status200, status500 int64

	// Create a worker pool
	var wg sync.WaitGroup
	for w := 0; w < *numWorkers; w++ {
		wg.Add(1)
		go worker(w, jobs, client, &sentTxCounter, &status200, &status500, &wg)
	}

	// Generate requests
	go func() {
		currentTPS := *startTPS
		for elapsed := 0; elapsed < *testDuration; elapsed += 10 {
			for sec := 0; sec < 10; sec++ {
				var batch []RPCRequest
				for i := 0; i < currentTPS; i++ {
					randomTxID := generateRandomString(*txSize)
					encodedTx := encodeToBase64(fmt.Sprintf("tx_id=%s", randomTxID))
					request := RPCRequest{
						JSONRPC: "2.0",
						Method:  "broadcast_tx_async",
						Params:  []string{encodedTx},
						ID:      elapsed*currentTPS + sec*currentTPS + i + 1,
					}

					batch = append(batch, request)

					// Send batch if it reaches the batch size
					if len(batch) == *batchSize {
						jobs <- batch
						batch = nil
					}
				}

				// Send remaining batch
				if len(batch) > 0 {
					jobs <- batch
				}

				// Sleep for 1 second before generating the next batch
				time.Sleep(1 * time.Second)
			}

			// Increment TPS after 10 seconds
			currentTPS += *step
		}
		close(jobs)
	}()

	// TPS measurement goroutine
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		var lastCount int64
		for elapsed := 0; elapsed < *testDuration; elapsed++ {
			<-ticker.C
			currentCount := sentTxCounter
			tpsValue := currentCount - lastCount
			lastCount = currentCount

			fmt.Printf("Elapsed Time: %ds, Current TPS: %d, Total Sent: %d, 200 OK: %d, 500 Errors: %d\n",
				elapsed+1, tpsValue, currentCount, status200, status500)
		}
	}()

	// Wait for all workers to finish
	wg.Wait()

	fmt.Printf("Test completed. Total Sent: %d, 200 OK: %d, 500 Errors: %d\n",
		sentTxCounter, status200, status500)
}
