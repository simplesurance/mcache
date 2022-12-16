package main

import "fmt"

func main() {
	v := []int{11, 2, 12, 10}
	var i int
	for i = len(v); i > 0; i-- {
		if v[i-1] == 10 {
			break
		}
	}

	fmt.Printf("final: i=%d %v\n", i, v[i:])
}
