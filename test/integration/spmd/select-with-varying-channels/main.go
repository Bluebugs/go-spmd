// run -goexperiment spmd -target=wasi
//
// Example demonstrating select statements with channels carrying varying values.
// Shows channel-based SPMD processing with concurrent data flows.
// Avoids time.Sleep (unsupported in WASI) and select+default spin loops
// (incompatible with cooperative scheduling).
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

// basicSelectExample demonstrates select with varying channels using
// buffered channels and ok-idiom for termination.
func basicSelectExample() {
	fmt.Println("=== Basic Select with Varying Channels ===")

	inputCh := make(chan lanes.Varying[int], 5)
	outputCh := make(chan lanes.Varying[int], 5)

	// Pre-fill buffered channel
	inputCh <- lanes.From([]int{1, 2, 3, 4})
	inputCh <- lanes.From([]int{10, 20, 30, 40})
	inputCh <- lanes.From([]int{100, 200, 300, 400})
	close(inputCh)

	// Drain input, process, forward to output
	for data := range inputCh {
		result := data * 2
		fmt.Printf("Received: %v -> Processed: %v\n", data, result)
		outputCh <- result
	}
	close(outputCh)

	fmt.Println("Output channel contents:")
	for result := range outputCh {
		fmt.Printf("Final result: %v\n", result)
	}
}

// pipelineExample demonstrates a multi-stage pipeline where each stage
// is a goroutine processing varying values through channels.
func pipelineExample() {
	fmt.Println("\n=== SPMD Pipeline Processing ===")

	stage1Ch := make(chan lanes.Varying[int], 5)
	stage2Ch := make(chan lanes.Varying[int], 5)
	stage3Ch := make(chan lanes.Varying[int], 5)
	doneCh := make(chan bool)

	// Stage 1: Data generator
	go func() {
		defer close(stage1Ch)
		for i := 0; i < 4; i++ {
			data := lanes.From([]int{i*4 + 1, i*4 + 2, i*4 + 3, i*4 + 4})
			stage1Ch <- data
			fmt.Printf("Stage 1 generated: %v\n", data)
		}
	}()

	// Stage 2: Add 10 to each lane (blocking receive, no spin)
	go func() {
		defer close(stage2Ch)
		for data := range stage1Ch {
			processed := data + 10
			stage2Ch <- processed
			fmt.Printf("Stage 2 processed: %v\n", processed)
		}
	}()

	// Stage 3: Multiply by 2 (blocking receive, no spin)
	go func() {
		defer close(stage3Ch)
		defer func() { doneCh <- true }()
		for data := range stage2Ch {
			final := data * 2
			stage3Ch <- final
			fmt.Printf("Stage 3 final: %v\n", final)
		}
	}()

	// Collect final results
	go func() {
		for result := range stage3Ch {
			total := reduce.Add(result)
			max := reduce.Max(result)
			fmt.Printf("Pipeline result: %v (sum: %d, max: %d)\n", result, total, max)
		}
		doneCh <- true
	}()

	<-doneCh // Stage 3 done
	<-doneCh // Result collection done
	fmt.Println("Pipeline processing completed")
}

// mixedChannelExample demonstrates select with both uniform and varying channels.
func mixedChannelExample() {
	fmt.Println("\n=== Mixed Uniform/Varying Channel Select ===")

	varyingCh := make(chan lanes.Varying[int], 3)
	uniformCh := make(chan int, 3)
	doneCh := make(chan bool, 1)

	// Pre-fill channels
	varyingCh <- lanes.From([]int{1, 2, 3, 4})
	uniformCh <- 100
	varyingCh <- lanes.From([]int{5, 6, 7, 8})
	doneCh <- true

	// Process with mixed channel types using select
	for {
		select {
		case vData := <-varyingCh:
			fmt.Printf("Received varying: %v\n", vData)
			result := vData + 50
			fmt.Printf("Varying processed: %v\n", result)

		case uData := <-uniformCh:
			fmt.Printf("Received uniform: %d\n", uData)
			vData := lanes.Varying[int](uData)
			fmt.Printf("Broadcast to varying: %v\n", vData)

		case <-doneCh:
			fmt.Println("Done signal received")
			return
		}
	}
}

func main() {
	fmt.Println("=== SPMD Select with Varying Channels Examples ===")

	basicSelectExample()
	pipelineExample()
	mixedChannelExample()

	fmt.Println("\nAll select with varying channels tests completed successfully")
}
