// Command filejournal writes a saga journal to a JSON-lines file and reads it back.
package main

import "os"

func main() { os.Exit(execute(os.Stdout, os.Stderr)) }
