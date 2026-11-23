// Command operator runs an order that stops after the pivot and reads the journal to say what is left.
package main

import "os"

func main() { os.Exit(execute(os.Stdout, os.Stderr)) }
