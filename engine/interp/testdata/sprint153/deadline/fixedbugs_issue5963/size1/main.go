package main

import "fmt"

const size = 1

func main() { c := make(chan int, 1); defer func() { c <- size }(); go func() { fmt.Println(<-c) }() }
