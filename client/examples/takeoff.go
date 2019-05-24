// +build example
//
// Do not build by default.

package main

import (
	"fmt"
	"time"

	"gobot.io/x/gobot/platforms/parrot/bebop/client"
)

func main() {
	//New will return a *Bebop, which is a struct containing things to control the drone like ports to use, ip address, networkFrameGenerator(), and so on
	bebop := client.New()

	if err := bebop.Connect(); err != nil {
		fmt.Println(err)
		return
	}

	bebop.HullProtection(true)

	fmt.Println("takeoff")
	if err := bebop.TakeOff(); err != nil {
		fmt.Println(err)
		fmt.Println("fail")
		return
	}
	time.Sleep(5 * time.Second)
	fmt.Println("land")
	if err := bebop.Land(); err != nil {
		fmt.Println(err)
		return
	}

	time.Sleep(5 * time.Second)
	fmt.Println("done")
}
