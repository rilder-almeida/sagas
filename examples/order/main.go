// Command order runs three orders through parallel steps, one of them with a state conflict.
package main

import "os"

func main() { os.Exit(execute(os.Stdout, os.Stderr)) }
