// Command order runs three orders through parallel steps, and New refuses one, whose parallel steps produce one type.
package main

import "os"

func main() { os.Exit(execute(os.Stdout, os.Stderr)) }
