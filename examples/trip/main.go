// Command trip runs a trip saga once to completion and once to compensation.
package main

import "os"

func main() { os.Exit(execute(os.Stdout, os.Stderr)) }
