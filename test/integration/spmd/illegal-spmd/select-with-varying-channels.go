// ILLEGAL: Varying channel types (lanes.Varying[chan T]) are not supported
// Expected error: "varying channel type not supported" or "cannot declare varying channel"
// Note: channels carrying varying values (chan lanes.Varying[T]) are LEGAL
package main

import (
	"lanes"
	"time"
)

func main() {
	// ILLEGAL: Declaring varying channel type
	var ch lanes.Varying[chan int]  // ERROR: lanes.Varying[chan T] syntax not supported

	select {
	case data := <-ch:  // ERROR: cannot use varying channel in select statement
		process(data)
	case <-time.After(time.Second):
		timeout()
	}
}

// ILLEGAL: Function returning varying channel type
func getVaryingChannel() lanes.Varying[chan string] {
	return nil
}

func illegalChannelSelect() {
	ch := getVaryingChannel()

	select {
	case msg := <-ch:  // ERROR: cannot use varying channel in select statement
		handleMessage(msg)
	default:
		noMessage()
	}
}

// ILLEGAL: Array of varying channels
func arrayChannelSelect() {
	channels := make([]lanes.Varying[chan int], 4)

	for i := range len(channels) {
		var current_ch lanes.Varying[chan int] = channels[i]

		select {
		case value := <-current_ch:  // ERROR: cannot use varying channel in select statement
			handleValue(value)
		default:
			handleEmpty()
		}
	}
}

// ILLEGAL: Struct fields with varying channel types
type ChannelStruct struct {
	varyingCh lanes.Varying[chan int]  // ERROR: lanes.Varying[chan T] not supported
}

// ILLEGAL: Map with varying channel types
func mapChannelExample() {
	var chMap map[string]lanes.Varying[chan int]  // ERROR: lanes.Varying[chan T] not supported
	_ = chMap
}

// ILLEGAL: Interface with varying channel types
type ChannelInterface interface {
	GetChannel() lanes.Varying[chan int]  // ERROR: lanes.Varying[chan T] not supported
}

// ILLEGAL: Function parameters with varying channel types
func sendToVaryingChannel(ch lanes.Varying[chan int], data int) {  // ERROR: lanes.Varying[chan T] parameter
	ch <- data
}

func receiveFromVaryingChannel(ch lanes.Varying[chan int]) int {  // ERROR: lanes.Varying[chan T] parameter
	return <-ch
}

// Helper functions
func process(data int)     {}
func timeout()             {}
func handleMessage(msg string) {}
func noMessage()           {}
func handleValue(value int) {}
func handleEmpty()         {}
