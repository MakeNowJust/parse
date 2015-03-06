[![GoDoc](http://godoc.org/github.com/tdewolff/parse/js?status.svg)](http://godoc.org/github.com/tdewolff/parse/js)

# JS
This package is a JS tokenizer (ECMA-262, edition 5.1) written in [Go][1]. It follows the specification at [ECMAScript Language Specification](http://www.ecma-international.org/ecma-262/5.1/). The tokenizer takes an io.Reader and converts it into tokens until the EOF.

## Installation
Run the following command

	go get github.com/tdewolff/parse/js

or add the following import and run project with `go get`

	import "github.com/tdewolff/parse/js"

## Tokenizer
### Usage
The following initializes a new tokenizer with io.Reader `r`:
``` go
z := js.NewTokenizer(r)
```

To tokenize until EOF an error, use:
``` go
for {
	tt, text := z.Next()
	switch tt {
	case js.ErrorToken:
		// error or EOF set in z.Err()
		return
	// ...
	}
}
```

All tokens (see [ECMAScript Language Specification](http://www.ecma-international.org/ecma-262/5.1/)):
``` go
ErrorToken          TokenType = iota // extra token when errors occur
WhitespaceToken                      // space \t \v \f
LineTerminatorToken                  // \r \n \r\n
CommentToken
IdentifierToken
PunctuatorToken /* { } ( ) [ ] . ; , < > <= >= == != === !==  + - * % ++ -- << >>
   >>> & | ^ ! ~ && || ? : = += -= *= %= <<= >>= >>>= &= |= ^= / /= */
BoolToken // true false
NullToken // null
NumericToken
StringToken
RegexpToken
```

### Quirks
Because the ECMAScript specification for `PunctuatorToken` (of which the `/` and `/=` symbols) and `RegexpToken` depends on a parser state to differentiate between the two, the tokenizer (to remain modular) uses different rules. Whenever `/` is encountered and the previous token is one of `(,=:[!&|?{};`, it returns a `RegexpToken`, otherwise it returns a `PunctuatorToken`. This is the same rule JSLint appears to use.

### Examples
``` go
package main

import (
	"os"

	"github.com/tdewolff/parse/js"
)

// Tokenize JS from stdin.
func main() {
	z := js.NewTokenizer(os.Stdin)
	for {
		tt, text := z.Next()
		switch tt {
		case js.ErrorToken:
			if z.Err() != io.EOF {
				fmt.Println("Error on line", z.Line(), ":", z.Err())
			}
			return
		case js.IdentifierToken:
			fmt.Println("Identifier", string(text))
		case js.NumericToken:
			fmt.Println("Numeric", string(text))
		// ...
		}
	}
}
```

## License
Released under the [MIT license](LICENSE.md).

[1]: http://golang.org/ "Go Language"