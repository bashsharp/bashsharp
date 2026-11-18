package main

import "fmt"

func pointer() *int        { return nil }
func slice() []int         { return nil }
func mapping() map[int]int { return nil }
func channel() chan int    { return nil }
func function() func()     { return nil }
func iface() any           { return nil }

func main() {
	var p *int = nil
	var s []int = nil
	var m map[int]int = nil
	var c chan int = nil
	var f func() = nil
	var i any = nil
	fmt.Println(p == nil)
	fmt.Println(s == nil)
	fmt.Println(m == nil)
	fmt.Println(c == nil)
	fmt.Println(f == nil)
	fmt.Println(i == nil)
	fmt.Println(pointer() == nil)
	fmt.Println(slice() == nil)
	fmt.Println(mapping() == nil)
	fmt.Println(channel() == nil)
	fmt.Println(function() == nil)
	fmt.Println(iface() == nil)
}
