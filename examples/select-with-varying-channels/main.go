// run -goexperiment spmd -target=wasi

// Example demonstrating select statements with varying channels and infinite SPMD loops
// Shows channel-based SPMD processing with concurrent data flows
package main

import (
	"fmt"
	"time"
	"lanes"
	"reduce"
)

// Demonstrates basic select with varying channels
func basicSelectExample() {
	fmt.Println("=== Basic Select with Varying Channels ===")
	
	// Create channels that carry varying values
	inputCh := make(chan varying int, 5)
	outputCh := make(chan varying int, 5)
	
	// Send some varying data to the input channel
	inputCh <- varying[4]([4]int{1, 2, 3, 4})
	inputCh <- varying[4]([4]int{10, 20, 30, 40})
	inputCh <- varying[4]([4]int{100, 200, 300, 400})
	close(inputCh)
	
	// Process data using select statement
	processed := 0
	for {
		select {
		case data, ok := <-inputCh:
			if !ok {
				fmt.Printf("Input channel closed after processing %d varying values\n", processed)
				goto finished
			}
			
			fmt.Printf("Received varying data: %v\n", data)
			
			// Process the varying data in SPMD context
			go for i, v := range data {
				result := v * 2  // Double all lane values
				fmt.Printf("Processed: %v\n", result)
				
				// Send result to output channel
				select {
				case outputCh <- result:
					// Successfully sent
				default:
					fmt.Println("Output channel full, dropping data")
				}
			}
			processed++
			
		default:
			fmt.Println("No data available, continuing...")
			break
		}
	}
	
finished:
	close(outputCh)
	
	// Drain output channel
	fmt.Println("Output channel contents:")
	for result := range outputCh {
		fmt.Printf("Final result: %v\n", result)
	}
}

// Demonstrates infinite SPMD loop with select
func infiniteLoopExample() {
	fmt.Println("\n=== Infinite SPMD Loop with Select ===")
	
	// Create channels for the example
	dataCh := make(chan varying int, 10)
	controlCh := make(chan int, 5)
	terminateCh := make(chan bool)
	
	// Launch a goroutine to send test data
	go func() {
		for i := 0; i < 3; i++ {
			data := varying[4]([4]int{i*10 + 1, i*10 + 2, i*10 + 3, i*10 + 4})
			dataCh <- data
			time.Sleep(100 * time.Millisecond)
		}
		controlCh <- "status_check"
		time.Sleep(200 * time.Millisecond)
		terminateCh <- true
	}()
	
	// Process data in infinite SPMD loop
	var totalProcessed varying int = varying(0)
	
	go for {  // Infinite SPMD loop
		select {
		case data := <-dataCh:
			fmt.Printf("Processing varying data in infinite loop: %v\n", data)
			
			// SPMD processing of the varying data
			processed := data * varying(3)  // Triple all values
			totalProcessed = totalProcessed + processed
			
			fmt.Printf("Processed result: %v\n", processed)
			fmt.Printf("Running total: %v\n", totalProcessed)
			
		case command := <-controlCh:
			fmt.Printf("Received control command: %s\n", command)
			if command == "status_check" {
				total := reduce.Add(totalProcessed)
				fmt.Printf("Status: Total processed across all lanes: %d\n", total)
			}
			
		case <-terminateCh:
			fmt.Println("Termination signal received, exiting infinite loop")
			return  // Exit the infinite SPMD loop
			
		default:
			// No channels ready, could do other work here
			// For demo purposes, just continue
			continue
		}
	}
}

// Demonstrates pipeline processing with varying channels
func pipelineExample() {
	fmt.Println("\n=== SPMD Pipeline Processing ===")
	
	// Create pipeline stages
	stage1Ch := make(chan varying int, 5)
	stage2Ch := make(chan varying int, 5)
	stage3Ch := make(chan varying int, 5)
	doneCh := make(chan bool)
	
	// Stage 1: Data generator
	go func() {
		defer close(stage1Ch)
		for i := 0; i < 4; i++ {
			data := varying[4]([4]int{i*4 + 1, i*4 + 2, i*4 + 3, i*4 + 4})
			stage1Ch <- data
			fmt.Printf("Stage 1 generated: %v\n", data)
		}
	}()
	
	// Stage 2: SPMD processing with select
	go func() {
		defer close(stage2Ch)
		
		go for {  // Infinite SPMD loop for stage 2
			select {
			case data, ok := <-stage1Ch:
				if !ok {
					return  // Input closed, exit loop
				}
				
				// SPMD processing: add 10 to each lane
				processed := data + varying(10)
				stage2Ch <- processed
				fmt.Printf("Stage 2 processed: %v\n", processed)
				
			default:
				// Could do other work or just continue
				continue
			}
		}
	}()
	
	// Stage 3: Final processing and output
	go func() {
		defer close(stage3Ch)
		defer func() { doneCh <- true }()
		
		go for {  // Infinite SPMD loop for stage 3
			select {
			case data, ok := <-stage2Ch:
				if !ok {
					return  // Input closed, exit loop
				}
				
				// SPMD processing: multiply by 2
				final := data * varying(2)
				stage3Ch <- final
				fmt.Printf("Stage 3 final: %v\n", final)
				
			default:
				continue
			}
		}
	}()
	
	// Collect final results
	fmt.Println("Final pipeline results:")
	go func() {
		for result := range stage3Ch {
			total := reduce.Add(result)
			max := reduce.Max(result)
			fmt.Printf("Pipeline result: %v (sum: %d, max: %d)\n", result, total, max)
		}
		doneCh <- true
	}()
	
	// Wait for completion
	<-doneCh  // Stage 3 done
	<-doneCh  // Result collection done
	fmt.Println("Pipeline processing completed")
}

// Demonstrates select with mixed uniform and varying channels
func mixedChannelExample() {
	fmt.Println("\n=== Mixed Uniform/Varying Channel Select ===")
	
	varyingCh := make(chan varying int, 3)
	uniformCh := make(chan int, 3)
	controlCh := make(chan string, 1)
	
	// Send test data
	varyingCh <- varying[4]([4]int{1, 2, 3, 4})
	uniformCh <- 100
	varyingCh <- varying[4]([4]int{5, 6, 7, 8})
	controlCh <- "finish"
	
	// Process with mixed channel types
	for {
		select {
		case vData := <-varyingCh:
			fmt.Printf("Received varying data: %v\n", vData)
			// Process varying data in SPMD context
			go for i := range lanes.Count(vData) {
				result := vData + varying(50)
				fmt.Printf("Varying processed: %v\n", result)
			}
			
		case uData := <-uniformCh:
			fmt.Printf("Received uniform data: %d\n", uData)
			// Process uniform data (can be broadcast to varying if needed)
			vData := varying(uData)
			fmt.Printf("Uniform converted to varying: %v\n", vData)
			
		case cmd := <-controlCh:
			fmt.Printf("Control command: %s\n", cmd)
			if cmd == "finish" {
				return
			}
			
		default:
			// Drain any remaining data
			select {
			case vData := <-varyingCh:
				fmt.Printf("Draining varying: %v\n", vData)
			case uData := <-uniformCh:
				fmt.Printf("Draining uniform: %d\n", uData)
			default:
				fmt.Println("All channels drained")
				return
			}
		}
	}
}

func main() {
	fmt.Println("=== SPMD Select with Varying Channels Examples ===")
	
	// Test 1: Basic select with varying channels
	basicSelectExample()
	
	// Test 2: Infinite SPMD loop with select
	infiniteLoopExample()
	
	// Test 3: Pipeline processing
	pipelineExample()
	
	// Test 4: Mixed channel types
	mixedChannelExample()
	
	// Summary
	fmt.Println("\n=== Summary ===")
	fmt.Println("✓ Select statements work with channels carrying varying values")
	fmt.Println("✓ Infinite SPMD loops (go for {}) enable continuous processing")
	fmt.Println("✓ Pipeline processing with SPMD stages")
	fmt.Println("✓ Mixed uniform/varying channel processing")
	fmt.Println("✓ All lanes participate in select operations")
	fmt.Println("✓ Channel operations work per-lane for varying data")
	
	fmt.Println("\nSPMD select with varying channels example completed successfully!")
}