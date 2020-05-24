/*
Copyright 2014 Google Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// The gitbrute command brute-forces a git commit hash prefix.
package main

import (
	"bytes"
	"crypto/sha1"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

var (
	pattern    = flag.String("pattern", "^[01]{7}", "Desired pattern")
	force      = flag.Bool("force", false, "Re-run, even if current hash matches pattern")
	cpu        = flag.Int("cpus", runtime.NumCPU(), "Number of CPUs to use. Defaults to number of processors.")
	nonceName  = flag.String("nonce-name", "nonce", "Name of nonce field to add to commit object")
	nonceChars = flag.String("nonce-chars", "0123456789", "Characters set to use in nonce field value")

	re *regexp.Regexp
)

// try is a nonce to brute force, looking for a matching commit.
type try struct {
	nonce int
}

// explore yields a sequence of try values with increasing nonces
func explore(c chan<- try) {
	var t try
	for {
		t.nonce++
		c <- t
		if t.nonce == 0 {
			break
		}
	}
	close(c)
}

// hexInPlace takes a slice of binary data and returns the same slice with double
// its length, hex-ified in-place.
func hexInPlace(v []byte) []byte {
	const hex = "0123456789abcdef"
	h := v[:len(v)*2]
	for i := len(v) - 1; i >= 0; i-- {
		b := v[i]
		h[i*2+0] = hex[b>>4]
		h[i*2+1] = hex[b&0xf]
	}
	return h
}

func fixHeader(obj []byte) []byte {
	obj = obj[bytes.IndexByte(obj, '\x00')+1:]
	obj = []byte(fmt.Sprintf("commit %d\x00%s", len(obj), obj))
	return obj
}

func addOrFindNonce(obj []byte) ([]byte, int) {
	nonceHeader := "\n" + *nonceName + " "
	if !bytes.Contains(obj, []byte(nonceHeader)) {
		i := bytes.Index(obj, []byte("\n\n"))
		header := obj[:i]
		footer := obj[i:]
		obj = []byte(fmt.Sprintf("%s%s%s", header, nonceHeader, footer))
		obj = fixHeader(obj)
	}
	i := bytes.Index(obj, []byte(nonceHeader))
	for obj[i+1] != '\n' {
		i++
	}
	return obj, i
}

func growNonce(obj []byte) []byte {
	nonceHeader := "\n" + *nonceName + " "
	i := bytes.Index(obj, []byte(nonceHeader)) + len(nonceHeader)
	newObj := make([]byte, 0, len(obj)+1)
	newObj = append(newObj, obj[:i]...)
	newObj = append(newObj, '0')
	newObj = append(newObj, obj[i:]...)
	newObj = fixHeader(newObj)
	return newObj
}

func bruteForce(obj []byte, winner chan<- []byte, possibilities <-chan try, done <-chan struct{}) {
	var nonceIdx int
	obj, nonceIdx = addOrFindNonce(obj)

	s1 := sha1.New()
	hexBuf := make([]byte, 0, sha1.Size*2)

	for t := range possibilities {
		select {
		case <-done:
			return
		default:
		writeNonce:
			i := nonceIdx
			nonce := t.nonce
			for nonce > 0 {
				if obj[i] == ' ' {
					obj = growNonce(obj)
					nonceIdx++
					goto writeNonce
				}
				obj[i] = (*nonceChars)[nonce%len(*nonceChars)]
				nonce /= len(*nonceChars)
				i--
			}

			s1.Reset()
			s1.Write(obj)
			if !re.Match(hexInPlace(s1.Sum(hexBuf[:0]))) {
				continue
			}

			winner <- obj
			return
		}
	}
}

func main() {
	flag.Parse()
	runtime.GOMAXPROCS(*cpu)

	var err error
	re, err = regexp.Compile(*pattern)
	if err != nil {
		log.Fatalf("Pattern %q is not a valid regexp: %s", err)
	}

	head, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		log.Fatal(err)
	}

	if re.MatchString(strings.TrimSpace(string(head))) {
		fmt.Println("gitbrute:", strings.TrimSpace(string(head)), "(already matches)")
		return
	}

	obj, err := exec.Command("git", "cat-file", "-p", "HEAD").Output()
	if err != nil {
		log.Fatal(err)
	}
	obj = fixHeader(obj)

	possibilities := make(chan try, 512)
	go explore(possibilities)

	winner := make(chan []byte)
	done := make(chan struct{})

	for i := 0; i < *cpu; i++ {
		buf := make([]byte, len(obj))
		copy(buf, obj)
		go bruteForce(buf, winner, possibilities, done)
	}

	w := <-winner
	close(done)

	var hash bytes.Buffer
	cmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--stdin")
	cmd.Stdout = &hash
	cmd.Stderr = os.Stderr
	cmd.Stdin = bytes.NewBuffer(w[bytes.IndexByte(w, '\x00')+1:])
	if err := cmd.Run(); err != nil {
		log.Fatalf("hash-object: %v", err)
	}

	reflogMsg := strings.Join(os.Args, " ")
	cmd = exec.Command("git", "update-ref", "-m", reflogMsg, "--create-reflog", "HEAD", strings.TrimSpace(hash.String()))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("update-ref: %v", err)
	}

	fmt.Println("gitbrute:", strings.TrimSpace(hash.String()))
}
