// Command deadline cancels an order at its deadline and shows the grace period and the compensation policies.
package main

import "os"

func main() { os.Exit(execute(os.Stdout, os.Stderr)) }
