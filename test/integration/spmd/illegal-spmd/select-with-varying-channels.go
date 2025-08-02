// ILLEGAL: Varying channel types (varying chan T) are not supported
// Expected error: "varying channel type not supported" or "cannot declare varying channel"
// Note: channels carrying varying values (chan varying T) are LEGAL
package main

import (
	"time"
)

func main() {
	// ILLEGAL: Declaring varying channel type
	var ch varying chan int  // ERROR: varying chan T syntax not supported
	
	select {
	case data := <-ch:  // ERROR: cannot use varying channel in select statement
		process(data)
	case <-time.After(time.Second):
		timeout()
	}
}

// ILLEGAL: Function returning varying channel type
func getVaryingChannel() varying chan string {
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
	channels := make([]varying chan int, 4)
	
	for i := range len(channels) {
		var current_ch varying chan int = channels[i]
		
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
	varyingCh varying chan int  // ERROR: varying chan T not supported
}

// ILLEGAL: Map with varying channel types  
func mapChannelExample() {
	var chMap map[string]varying chan int  // ERROR: varying chan T not supported
	_ = chMap
}

// ILLEGAL: Interface with varying channel types
type ChannelInterface interface {
	GetChannel() varying chan int  // ERROR: varying chan T not supported
}

// ILLEGAL: Function parameters with varying channel types
func sendToVaryingChannel(ch varying chan int, data int) {  // ERROR: varying chan T parameter
	ch <- data
}

func receiveFromVaryingChannel(ch varying chan int) int {  // ERROR: varying chan T parameter  
	return <-ch
}

// Helper functions
func process(data int)     {}
func timeout()             {}
func handleMessage(msg string) {}
func noMessage()           {}
func handleValue(value int) {}
func handleEmpty()         {}