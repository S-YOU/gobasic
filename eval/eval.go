// Package eval contains our evaluator
//
// This is pretty simple:
//
//  * The program is an array of tokens.
//
//  * We have one statement per line.
//
//  * We handle the different types of statements in their own functions.
//
package eval

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/skx/gobasic/object"
	"github.com/skx/gobasic/token"
	"github.com/skx/gobasic/tokenizer"
)

// Interpreter holds our state.
type Interpreter struct {

	// The program we execute is nothing more than an array of tokens.
	program []token.Token

	// Should we finish execution?
	// This is set by the `END` statement.
	finished bool

	// We execute from the given offset.
	//
	// Sequential execution just means bumping this up by one each
	// time we execute an instruction, or pick off the arguments to
	// one.
	//
	// But set it to 17, or some other random value, and you've got
	// a GOTO implemented!
	offset int

	// We record the line-number we're currently executing here.
	// NOTE: This is a string because we take it from the lineno
	// token, with no modification.
	lineno string

	// A stack for handling GOSUB/RETURN calls
	gstack *Stack

	// vars holds the variables set in the program, via LET.
	vars *Variables

	// loops holds references to open FOR-loops
	loops *Loops

	// STDIN is an input-reader used for the INPUT statement
	STDIN *bufio.Reader

	// Hack: Was the previous statement a GOTO/GOSUB?
	jump bool

	// lines is a lookup table - the key is the line-number of
	// the source program, and the value is the offset in our
	// program-array that this is located at.
	lines map[string]int

	// functions holds builtin-functions
	functions *Builtins

	// trace is true if the user is tracing execution
	trace bool
}

// New is our constructor.
//
// Given a lexer we store all the tokens it produced in our array, and
// initialise some other state.
func New(stream *tokenizer.Tokenizer) *Interpreter {
	t := &Interpreter{offset: 0}

	// setup a stack for holding line-numbers for GOSUB/RETURN
	t.gstack = NewStack()

	// setup storage for variable-contents
	t.vars = NewVars()

	// setup storage for for-loops
	t.loops = NewLoops()

	// Built-in functions are stored here.
	t.functions = NewBuiltins()

	// allow reading from STDIN
	t.STDIN = bufio.NewReader(os.Stdin)

	//
	// Setup a map to hold our jump-targets
	//
	t.lines = make(map[string]int)

	//
	// Save the tokens that our program consists of, one by one,
	// until we hit the end.
	//
	// We also record the offset at which each line starts, which
	// means that the GOTO & GOSUB statements don't need to scan
	// the program from start to finish to find the destination
	// to jump to.
	//
	offset := 0
	for {
		tok := stream.NextToken()
		if tok.Type == token.EOF {
			break
		}

		// Did we find a line-number?
		if tok.Type == token.LINENO {

			// Save the offset in the map
			line := tok.Literal

			// Already an offset?  That means we
			// have duplicate line-numbers
			if t.lines[line] != 0 {
				fmt.Printf("WARN: Line %s is duplicated - GOTO/GOSUB behaviour is undefined\n", line)
			}
			t.lines[line] = offset
		}

		// Regardless append the token to our array
		t.program = append(t.program, tok)

		offset++
	}

	//
	// Add in our builtins.
	//
	// These are implemented in golang in the file builtins.go
	//
	// We have to do this after we've loaded our program, because
	// the registration involves rewriting our program.
	//
	t.RegisterBuiltin("ABS", 1, ABS)
	t.RegisterBuiltin("ACS", 1, ACS)
	t.RegisterBuiltin("ASN", 1, ASN)
	t.RegisterBuiltin("ATN", 1, ATN)
	t.RegisterBuiltin("BIN", 1, BIN)
	t.RegisterBuiltin("COS", 1, COS)
	t.RegisterBuiltin("EXP", 1, EXP)
	t.RegisterBuiltin("INT", 1, INT)
	t.RegisterBuiltin("LN", 1, LN)
	t.RegisterBuiltin("PI", 0, PI)
	t.RegisterBuiltin("RND", 1, RND)
	t.RegisterBuiltin("SGN", 1, SGN)
	t.RegisterBuiltin("SIN", 1, SIN)
	t.RegisterBuiltin("SQR", 1, SQR)
	t.RegisterBuiltin("TAN", 1, TAN)
	t.RegisterBuiltin("VAL", 1, VAL)

	// Primitives that operate upon strings
	t.RegisterBuiltin("CHR$", 1, CHR)
	t.RegisterBuiltin("CODE", 1, CODE)
	t.RegisterBuiltin("LEFT$", 2, LEFT)
	t.RegisterBuiltin("LEN", 1, LEN)
	t.RegisterBuiltin("MID$", 3, MID)
	t.RegisterBuiltin("RIGHT$", 2, RIGHT)
	t.RegisterBuiltin("TL$", 1, TL)
	t.RegisterBuiltin("STR$", 1, STR)

	t.RegisterBuiltin("DUMP", 1, DUMP)

	return t
}

// SetTrace allows the user to enable/disable tracing.
func (e *Interpreter) SetTrace(val bool) {
	e.trace = val
}

////
//
// Helpers for stuff
//
////

// factor
func (e *Interpreter) factor() object.Object {

	if e.offset >= len(e.program) {
		return object.Error("Hit end of program processing factor()")
	}

	tok := e.program[e.offset]
	switch tok.Type {
	case token.LBRACKET:
		// skip past the lbracket
		e.offset++

		// handle the expr
		ret := e.expr(true)
		if ret.Type() == object.ERROR {
			return ret
		}

		// skip past the rbracket
		tok = e.program[e.offset]
		if tok.Type != token.RBRACKET {
			return object.Error("Unclosed bracket around expression!")
		}
		e.offset++

		// Return the result of the sub-expression
		return (ret)
	case token.INT:
		i, err := strconv.ParseFloat(tok.Literal, 64)
		if err == nil {
			e.offset++
			return &object.NumberObject{Value: i}
		}
		return object.Error("Failed to convert %s -> float64 %s", tok.Literal, err.Error())

	case token.STRING:
		e.offset++
		return &object.StringObject{Value: tok.Literal}

	case token.BUILTIN:

		//
		// Call the built-in and return the value.
		//
		val := e.callBuiltin(tok.Literal)
		return val

	case token.IDENT:

		//
		// Get the contents of the variable.
		//
		val := e.GetVariable(tok.Literal)
		e.offset++
		return val
	}

	return object.Error("factor() - unhandled token: %v\n", tok)
}

// terminal - handles parsing of the form
//  ARG1 OP ARG2
//
// See also expr() which is similar.
func (e *Interpreter) term() object.Object {

	// First argument
	f1 := e.factor()

	if e.offset >= len(e.program) {
		return object.Error("Hit end of program processing term()")
	}

	// Get the operator
	tok := e.program[e.offset]

	// Here we handle the obvious ones.
	for tok.Type == token.ASTERISK ||
		tok.Type == token.SLASH ||
		tok.Type == token.MOD {

		// skip the operator
		e.offset++

		// get the second argument
		f2 := e.factor()

		//
		// We allow operations of the form:
		//
		//  NUMBER OP NUMBER
		//
		// We can error on strings.
		//
		if f1.Type() != object.NUMBER ||
			f2.Type() != object.NUMBER {
			return object.Error("term() only handles integers")
		}

		//
		// Get the values.
		//
		v1 := f1.(*object.NumberObject).Value
		v2 := f2.(*object.NumberObject).Value

		//
		// Handle the operator.
		//
		if tok.Type == token.ASTERISK {
			f1 = &object.NumberObject{Value: v1 * v2}
		}
		if tok.Type == token.SLASH {
			if v2 == 0 {
				return object.Error("Division by zero!")
			}
			f1 = &object.NumberObject{Value: v1 / v2}
		}
		if tok.Type == token.MOD {
			f1 = &object.NumberObject{Value: float64(int(v1) % int(v2))}
		}

		// repeat?
		tok = e.program[e.offset]
	}

	return f1
}

// expression - handles parsing of the form
//  ARG1 OP ARG2
// See also term() which is similar.
func (e *Interpreter) expr(allowBinOp bool) object.Object {

	// First argument.
	t1 := e.term()

	// Did this error?
	if t1.Type() == object.ERROR {
		return t1
	}

	if e.offset >= len(e.program) {
		return object.Error("Hit end of program processing expr()")
	}

	// Get the operator
	tok := e.program[e.offset]

	// Here we handle the obvious ones.
	for tok.Type == token.PLUS ||
		tok.Type == token.MINUS ||
		tok.Type == token.AND ||
		tok.Type == token.OR {

		//
		// Sometimes we disable binary AND + binary OR.
		//
		// This is mostly due to our naive parser, because
		// it gets confused handling "IF BLAH AND BLAH  .."
		//
		if allowBinOp == false {
			if tok.Type == token.AND ||
				tok.Type == token.OR {
				return t1
			}
		}

		// skip the operator
		e.offset++

		// Get the second argument.
		t2 := e.term()

		// Did this error?
		if t2.Type() == object.ERROR {
			return t2
		}

		//
		// We allow operations of the form:
		//
		//  NUMBER OP NUMBER
		//
		//  STRING OP STRING
		//
		// We support ZERO operations where the operand types
		// do not match.  If we hit this it's a bug.
		//
		if t1.Type() != t2.Type() {
			return object.Error("expr() - type mismatch between '%v' + '%v'", t1, t2)
		}

		//
		// Are the operands strings?
		//
		if t1.Type() == object.STRING {

			//
			// Get their values.
			//
			s1 := t1.(*object.StringObject).Value
			s2 := t2.(*object.StringObject).Value

			//
			// We only support "+" for concatenation
			//
			if tok.Type == token.PLUS {
				t1 = &object.StringObject{Value: s1 + s2}
			} else {
				return object.Error("expr() operation '%s' not supported for strings", tok.Literal)
			}

		} else {

			//
			// Here we have two operands that are numbers.
			//
			// Get their values for neatness.
			//
			n1 := t1.(*object.NumberObject).Value
			n2 := t2.(*object.NumberObject).Value

			if tok.Type == token.PLUS {
				t1 = &object.NumberObject{Value: n1 + n2}
			} else if tok.Type == token.MINUS {
				t1 = &object.NumberObject{Value: n1 - n2}
			} else if tok.Type == token.AND {
				t1 = &object.NumberObject{Value: float64(int(n1) & int(n2))}
			} else if tok.Type == token.OR {
				t1 = &object.NumberObject{Value: float64(int(n1) | int(n2))}
			} else {
				return object.Error("Token not handled for two numbers: %s\n", tok.Literal)
			}
		}

		// repeat?
		tok = e.program[e.offset]
	}

	return t1
}

// compare runs a comparison function (!)
//
// It is only used by the `IF` statement.
func (e *Interpreter) compare(allowBinOp bool) object.Object {

	// Get the first statement
	t1 := e.expr(allowBinOp)
	if t1.Type() == object.ERROR {
		return t1
	}

	// Get the comparison function
	op := e.program[e.offset]
	e.offset++

	// Get the second expression
	t2 := e.expr(allowBinOp)
	if t2.Type() == object.ERROR {
		return t2
	}

	//
	// String-tests here
	//
	if t1.Type() == object.STRING && t2.Type() == object.STRING {

		v1 := t1.(*object.StringObject).Value
		v2 := t2.(*object.StringObject).Value

		switch op.Type {
		case token.ASSIGN:
			if v1 == v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		case token.NOT_EQUALS:
			if v1 != v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		case token.GT:
			if v1 > v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		case token.GT_EQUALS:
			if v1 >= v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		case token.LT:
			if v1 < v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		case token.LT_EQUALS:
			if v1 <= v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		}
		// false
		return &object.NumberObject{Value: 0}
	}

	//
	// String-tests here
	//
	if t1.Type() == object.NUMBER && t2.Type() == object.NUMBER {

		v1 := t1.(*object.NumberObject).Value
		v2 := t2.(*object.NumberObject).Value

		switch op.Type {
		case token.ASSIGN:
			if v1 == v2 {
				//true
				return &object.NumberObject{Value: 1}
			}

		case token.GT:
			if v1 > v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		case token.GT_EQUALS:
			if v1 >= v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		case token.LT:
			if v1 < v2 {
				//true
				return &object.NumberObject{Value: 1}
			}

		case token.LT_EQUALS:
			if v1 <= v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		case token.NOT_EQUALS:
			if v1 != v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		}
		// false
		return &object.NumberObject{Value: 0}
	}

	return object.Error("Unhandled comparison: %v[%s] %v %v[%s]\n", t1, t1.Type(), op, t2, t2.Type())
}

// Call the built-in with the given name if we can.
func (e *Interpreter) callBuiltin(name string) object.Object {

	if e.trace {
		fmt.Printf("callBultin(%s)\n", name)
	}

	//
	// Fetch the function, so we know how many arguments
	// it should expect.
	//
	n, fun := e.functions.Get(name)

	//
	// skip past the function-call itself
	//
	e.offset++

	//
	// Each built-in takes a specific number of arguments.
	//
	// We pass only `string` or `number` to it.
	//
	var args []object.Object

	//
	// Build up the args, converting and evaluating as we go.
	//
	for len(args) < n {

		//
		// Get the next token, if it is a comma then eat it.
		//
		tok := e.program[e.offset]
		if tok.Type == token.COMMA {
			e.offset++
			continue
		}

		//
		// If we hit newline/eof then we're done.
		//
		// (And we've got an error, because we didn't receive as
		// many arguments as we expected.)
		//
		if tok.Type == token.NEWLINE {
			return (object.Error("Hit newline while searching for argument %d to %s", len(args)+1, name))
		}
		if tok.Type == token.EOF {
			return (object.Error("Hit EOF while searching for argument %d to %s", len(args)+1, name))
		}

		//
		// Evaluate the next expression.
		//
		obj := e.expr(true)

		//
		// If we found an error then return it.
		//
		if obj.Type() == object.ERROR {
			return obj
		}

		//
		// Append the argument to our list.
		//
		args = append(args, obj)

		//
		// Show our current progress.
		//
		if e.trace {
			fmt.Printf("\tArgument %d -> %s\n", len(args), obj.String())
		}
	}

	//
	// Actually call the function, now we have the correct number
	// of arguments to do so.
	//
	out := fun(*e, args)

	if e.trace {
		fmt.Printf("\tReturn value %s\n", out.String())
	}
	return out
}

////
//
// Statement-handlers
//
////

// runForLoop handles a FOR loop
func (e *Interpreter) runForLoop() error {
	// we expect "ID = NUM to NUM [STEP NUM]"

	// Bump past the FOR token
	e.offset++

	// We now expect a variable name.
	target := e.program[e.offset]
	e.offset++
	if target.Type != token.IDENT {
		return fmt.Errorf("Expected IDENT after FOR, got %v", target)
	}

	// Now an EQUALS
	eq := e.program[e.offset]
	e.offset++
	if eq.Type != token.ASSIGN {
		return fmt.Errorf("Expected = after 'FOR %s' , got %v", target.Literal, eq)
	}

	// Now an integer/variable
	startI := e.program[e.offset]
	e.offset++

	var start float64
	if startI.Type == token.INT {
		v, err := strconv.ParseFloat(startI.Literal, 64)
		if err != nil {
			return fmt.Errorf("Failed to convert %s to an int %s", startI.Literal, err.Error())
		}
		start = v
	} else if startI.Type == token.IDENT {

		x := e.GetVariable(startI.Literal)
		if x.Type() != object.NUMBER {
			return fmt.Errorf("FOR: start-variable must be an integer!")
		}
		start = x.(*object.NumberObject).Value
	} else {
		return fmt.Errorf("Expected INT/VARIABLE after 'FOR %s=', got %v", target.Literal, startI)
	}

	// Now TO
	to := e.program[e.offset]
	e.offset++
	if to.Type != token.TO {
		return fmt.Errorf("Expected TO after 'FOR %s=%s', got %v", target.Literal, startI, to)
	}

	// Now an integer/variable
	endI := e.program[e.offset]
	e.offset++

	var end int

	if endI.Type == token.INT {
		v, err := strconv.ParseFloat(endI.Literal, 64)
		if err != nil {
			return fmt.Errorf("Failed to convert %s to an int %s", endI.Literal, err.Error())
		}

		end = int(v)
	} else if endI.Type == token.IDENT {

		x := e.GetVariable(endI.Literal)
		if x.Type() != object.NUMBER {
			return fmt.Errorf("FOR: end-variable must be an integer!")
		}
		end = int(x.(*object.NumberObject).Value)
	} else {
		return fmt.Errorf("Expected INT/VARIABLE after 'FOR %s=%s TO', got %v", target.Literal, startI, endI)
	}

	// Default step is 1.
	stepI := "1"

	// Is the next token a step?
	if e.program[e.offset].Type == token.STEP {
		e.offset++

		s := e.program[e.offset]
		e.offset++
		if s.Type != token.INT {
			return fmt.Errorf("Expected INT after 'FOR %s=%s TO %s STEP', got %v", target.Literal, startI, endI, s)
		}
		stepI = s.Literal
	}

	step, err := strconv.ParseFloat(stepI, 64)
	if err != nil {
		return fmt.Errorf("Failed to convert %s to an int %s", stepI, err.Error())
	}

	//
	// Now we can record the important details of the for-loop
	// in a hash.
	//
	// The key observersions here are that all the magic
	// really involved in the FOR-loop happens at the point
	// you interpret the "NEXT X" section.
	//
	// Handling the NEXT statement involves:
	//
	//  Incrementing the step-variable
	//  Looking for termination
	//  If not over then "jumping back".
	//
	// So for a for-loop we just record the start/end conditions
	// and the address of the body of the loop - ie. the next
	// token - so that the next-handler can GOTO there.
	//
	// It is almost beautifully elegent.
	//
	f := ForLoop{id: target.Literal,
		offset: e.offset,
		start:  int(start),
		end:    int(end),
		step:   int(step)}

	//
	// Set the variable to the starting-value
	//
	e.SetVariable(target.Literal, &object.NumberObject{Value: start})

	//
	// And record our loop - keyed on the name of the variable
	// which is used as the index.  This allows easy and natural
	// nested-loops.
	//
	// Did I say this is elegent?
	//
	e.loops.Add(f)
	return nil
}

// runGOSUB handles a control-flow change
func (e *Interpreter) runGOSUB() error {

	// Skip the GOSUB-instruction itself
	e.offset++

	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing GOSUB")
	}

	// Get the target
	target := e.program[e.offset]

	// We expect the target to be an int
	if target.Type != token.INT {
		return (fmt.Errorf("ERROR: GOSUB should be followed by an integer"))
	}

	//
	// We want to store the return address on our GOSUB-stack,
	// so that the next RETURN will continue execution at the
	// next instruction.
	//
	// Because we only support one statement per-line we can
	// handle that by bumping forward.  That should put us on the
	// LINENO of the following-line.
	//
	e.offset++
	e.gstack.Push(e.offset)

	//
	// Lookup the offset of the given line-number in our program/
	//
	offset := e.lines[target.Literal]

	//
	// If we found it then use it.
	//
	if offset > 0 {
		e.offset = offset
		return nil
	}

	return fmt.Errorf("Failed to GOSUB %s", target.Literal)
}

// runGOTO handles a control-flow change
func (e *Interpreter) runGOTO() error {

	// Skip the GOTO-instruction
	e.offset++

	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing GOTO")
	}

	// Get the GOTO-target
	target := e.program[e.offset]

	// We expect the target to be an int
	if target.Type != token.INT {
		return fmt.Errorf("ERROR: GOTO should be followed by an integer")
	}

	//
	// Lookup the offset of the given line-number in our program/
	//
	offset := e.lines[target.Literal]

	//
	// If we found it then use it.
	//
	if offset > 0 {
		e.offset = offset
		return nil
	}

	return fmt.Errorf("Failed to GOTO %s", target.Literal)
}

// runINPUT handles input of numbers from the user.
//
// NOTE:
//   INPUT "Foo", a   -> Reads an integer
//   INPUT "Foo", a$  -> Reads a string
func (e *Interpreter) runINPUT() error {

	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing INPUT")
	}

	// Skip the INPUT-instruction
	e.offset++

	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing INPUT")
	}

	// Get the prompt
	prompt := e.program[e.offset]
	e.offset++

	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing INPUT")
	}

	// We expect a comma
	comma := e.program[e.offset]
	e.offset++
	if comma.Type != token.COMMA {
		return fmt.Errorf("ERROR: INPUT should be : INPUT \"prompt\",var")
	}

	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing INPUT")
	}

	// Now the ID
	ident := e.program[e.offset]
	e.offset++
	if ident.Type != token.IDENT {
		return fmt.Errorf("ERROR: INPUT should be : INPUT \"prompt\",var")
	}

	//
	// Print the prompt
	//
	fmt.Printf(prompt.Literal)

	//
	// Read the input from the user.
	//
	input, _ := e.STDIN.ReadString('\n')
	input = strings.TrimRight(input, "\n")

	//
	// Now we handle the type-conversion.
	//
	if strings.HasSuffix(ident.Literal, "$") {
		// We set a string
		e.SetVariable(ident.Literal, &object.StringObject{Value: input})
		return nil
	}

	// We set an int
	i, err := strconv.ParseFloat(input, 64)
	if err != nil {
		return err
	}

	//
	// Set the value
	//
	e.SetVariable(ident.Literal, &object.NumberObject{Value: i})
	return nil
}

// runIF handles conditional testing.
//
// There are a lot of choices to be made when it comes to IF, such as
// whether to support an ELSE section or not.  And what to allow
// inside the matching section generally:
//
// A single statement?
// A block?
//
// Here we _only_ allow:
//
//  IF $EXPR THEN $STATEMENT ELSE $STATEMENT NEWLINE
//
// $STATEMENT will only be a single expression
//
func (e *Interpreter) runIF() error {

	// Bump past the IF token
	e.offset++

	// Get the result of the comparison-function
	// against the two arguments.
	res := e.compare(false)

	// Error?
	if res.Type() == object.ERROR {
		return fmt.Errorf("%s", res.(*object.ErrorObject).Value)
	}

	//
	// We need a boolean result, so we convert here.
	//
	result := false
	if res.Type() == object.NUMBER {
		result = (res.(*object.NumberObject).Value == 1)
	}

	//
	// The general form of an IF statement is
	//  IF $COMPARE THEN .. ELSE .. NEWLINE
	//
	// However we also want to allow people to write:
	//
	//  IF A=3 OR A=4 THEN ..
	//
	// So we'll special case things here.
	//

	// We now expect THEN most of the time
	target := e.program[e.offset]
	e.offset++

	for target.Type == token.AND ||
		target.Type == token.OR {

		//
		// See what the next comparison looks like.
		//
		extra := e.compare(false)

		if extra.Type() == object.ERROR {
			return fmt.Errorf("%s", extra.(*object.ErrorObject).Value)
		}

		//
		// We need a boolean answer.
		//
		extraResult := false
		if extra.Type() == object.NUMBER {
			extraResult = (extra.(*object.NumberObject).Value == 1)
		}

		//
		// Update our result appropriately.
		//
		if target.Type == token.AND {
			result = result && extraResult
		}
		if target.Type == token.OR {
			result = result || extraResult
		}

		// Repeat?
		target = e.program[e.offset]
		e.offset++
	}

	//
	// Now we're in the THEN section.
	//
	if target.Type != token.THEN {
		return fmt.Errorf("Expected THEN after IF EXPR, got %v", target)
	}

	//
	// OK so if our comparison succeeded we can execute the single
	// statement between THEN + ELSE
	//
	// Otherwise between ELSE + Newline
	//
	if result {

		//
		// Execute single statement
		//
		e.RunOnce()

		//
		// Help me, I'm in Hell.
		//
		e.offset -= 1

		//
		// If the user made a jump then we'll
		// abort here, because if the single-statement modified our
		// flow control we're screwed.
		//
		// (Because we'll start searching from the NEW location.)
		//
		//
		if e.jump {
			return nil
		}

		//
		// We get the next token, it should either be ELSE + expr
		// or newline.
		//
		// Skip until we hit the end of line.
		//
		if e.offset >= len(e.program) {
			return fmt.Errorf("Hit end of program processing IF")
		}

		tmp := e.program[e.offset]
		e.offset++
		for tmp.Type != token.NEWLINE {

			if e.offset >= len(e.program) {
				return fmt.Errorf("Hit end of program processing IF")
			}
			tmp = e.program[e.offset]
			e.offset++
		}
	} else {

		//
		// Here the test failed.
		//
		// Skip over the truthy-condition until we either
		// hit ELSE, or the newline that will terminate our
		// IF-statement.
		//
		//
		for {

			if e.offset >= len(e.program) {
				return fmt.Errorf("Hit end of program processing IF")
			}

			tmp := e.program[e.offset]
			e.offset++

			// If we hit the newline then we're done
			if tmp.Type == token.NEWLINE {
				return nil
			}

			// Otherwise did we hit the else?
			if tmp.Type == token.ELSE {

				// Execute the single statement
				e.RunOnce()

				// Then return.
				return nil
			}
		}
	}

	return nil
}

// runLET handles variable creation/updating.
func (e *Interpreter) runLET() error {

	// Bump past the LET token
	e.offset++

	// We now expect an ID
	target := e.program[e.offset]
	e.offset++
	if target.Type != token.IDENT {
		return fmt.Errorf("Expected IDENT after LET, got %v", target)
	}

	// Now "="
	assign := e.program[e.offset]
	if assign.Type != token.ASSIGN {
		return fmt.Errorf("Expected assignment after LET x, got %v", assign)
	}
	e.offset++

	// now we're at the expression/value/whatever
	res := e.expr(true)

	// Did we get an error in the expression?
	if res.Type() == object.ERROR {
		return fmt.Errorf("%s", res.(*object.ErrorObject).Value)
	}

	// Store the result
	e.SetVariable(target.Literal, res)
	return nil
}

// runNEXT handles the NEXT statement
func (e *Interpreter) runNEXT() error {
	// Bump past the NEXT token
	e.offset++

	// Get the identifier
	target := e.program[e.offset]
	e.offset++
	if target.Type != token.IDENT {
		return fmt.Errorf("Expected IDENT after NEXT in FOR loop, got %v", target)
	}

	// OK we've found the tail of a loop
	//
	// We need to bump the value of the given variable by the offset
	// and compare it against the max.
	//
	// If the max hasn't finished we loop around again.
	//
	// If it has we remove the for-loop
	//
	data := e.loops.Get(target.Literal)
	if data.id == "" {
		return fmt.Errorf("NEXT %s found - without opening FOR", target.Literal)
	}

	//
	// Get the variable value, and increase it.
	//
	cur := e.GetVariable(target.Literal)
	if cur.Type() != object.NUMBER {
		return fmt.Errorf("NEXT variable %s is not a number!", target.Literal)
	}
	iVal := cur.(*object.NumberObject).Value

	//
	// If the start/end offsets are the same then
	// we terminate immediately.
	//
	if data.start == data.end {
		data.finished = true

		// updates-in-place.  bad name
		e.loops.Add(data)
	}

	//
	// Increment the number.
	//
	iVal += float64(data.step)

	//
	// Set it
	//
	e.SetVariable(target.Literal, &object.NumberObject{Value: iVal})

	//
	// Have we finnished?
	//
	if data.finished {
		e.loops.Remove(target.Literal)
		return nil
	}

	//
	// If we've reached our limit we mark this as complete,
	// but note that we dont' terminate to allow the actual
	// end-number to be inclusive.
	//
	if iVal == float64(data.end) {
		data.finished = true

		// updates-in-place.  bad name
		e.loops.Add(data)
	}

	//
	// Otherwise loop again
	//
	e.offset = data.offset
	return nil
}

// runPRINT handles a print!
// NOTE:
//  Print basically swallows input up to the next newline.
//  However it also stops at ":" to cope with the case of printing in an IF
func (e *Interpreter) runPRINT() error {

	// Bump past the PRINT token
	e.offset++

	// Now keep lookin for things to print until we hit a newline.
	for e.offset < len(e.program) {

		// Get the token
		tok := e.program[e.offset]

		// End of the line, or statement?
		if tok.Type == token.NEWLINE || tok.Type == token.COLON {
			return nil
		}

		// Printing a literal?
		if tok.Type == token.INT || tok.Type == token.STRING {
			fmt.Printf("%s", tok.Literal)
		} else if tok.Type == token.COMMA {
			fmt.Printf(" ")
		} else if tok.Type == token.BUILTIN {

			// Call the function.
			val := e.callBuiltin(tok.Literal)

			// Did it error?
			if val.Type() == object.ERROR {
				return fmt.Errorf("%s", val.(*object.ErrorObject).Value)
			}

			// Otherwise handle the output
			// 1.  String
			if val.Type() == object.STRING {
				fmt.Printf("%s", val.(*object.StringObject).Value)
			}
			// 2.  Number
			if val.Type() == object.NUMBER {
				n := val.(*object.NumberObject).Value

				// If the value is basically an
				// int then cast it to avoid
				// 3 looking like 3.0000
				if n == float64(int(n)) {
					fmt.Printf("%d", int(n))
				} else {
					fmt.Printf("%f", n)
				}
			}

			//
			// We're going to bump back one,
			// because callBuiltin will advance
			// to the end of the arguments.
			//
			e.offset--
		} else if tok.Type == token.IDENT {

			//
			// Get the variable.
			//
			val := e.GetVariable(tok.Literal)
			if val.Type() == object.ERROR {
				return fmt.Errorf("%s", val.(*object.ErrorObject).Value)
			}
			if val.Type() == object.STRING {
				fmt.Printf("%s", val.(*object.StringObject).Value)
			}
			if val.Type() == object.NUMBER {
				n := val.(*object.NumberObject).Value

				// If the value is basically an
				// int then cast it to avoid
				// 3 looking like 3.0000
				if n == float64(int(n)) {
					fmt.Printf("%d", int(n))
				} else {
					fmt.Printf("%f", n)
				}
			}
		} else {
			// OK we're not printing:
			//
			//  an int
			//  a string
			//  a variable
			//
			// As a fall-back we'll assume we've been given
			// an expression, and print the result.
			//
			out := e.expr(true)

			if out.Type() == object.STRING {
				fmt.Printf("%s", out.(*object.StringObject).Value)
			}
			if out.Type() == object.NUMBER {
				n := out.(*object.NumberObject).Value

				// If the value is basically an
				// int then cast it to avoid
				// 3 looking like 3.0000
				if n == float64(int(n)) {
					fmt.Printf("%d", int(n))
				} else {
					fmt.Printf("%f", n)
				}
			}
		}
		e.offset++
	}

	return nil
}

// REM handles a REM statement
//
// This merely swallows input until the following newline / EOF.
func (e *Interpreter) runREM() error {

	for e.offset < len(e.program) {
		tok := e.program[e.offset]
		if tok.Type == token.NEWLINE {
			return nil
		}
		e.offset++
	}

	return nil
}

// RETURN handles a control-flow operation
func (e *Interpreter) runRETURN() error {

	// Stack can't be empty
	if e.gstack.Empty() {
		return fmt.Errorf("RETURN without GOSUB")
	}

	// Get the return address
	ret, err := e.gstack.Pop()
	if err != nil {
		return fmt.Errorf("Error handling RETURN: %s", err.Error())
	}

	// Return execution where we left off.
	e.offset = ret
	return nil
}

////
//
// Our core public API
//
////

// RunOnce executes a single statement.
func (e *Interpreter) RunOnce() error {

	//
	// Get the current token
	//
	tok := e.program[e.offset]
	var err error

	if e.trace {
		fmt.Printf("RunOnce( %s )\n", tok.String())
	}

	e.jump = false

	//
	// Handle this token
	//
	switch tok.Type {
	case token.NEWLINE:
		// NOP
	case token.LINENO:
		e.lineno = tok.Literal
	case token.END:
		e.finished = true
		return nil
	case token.FOR:
		err = e.runForLoop()
	case token.GOSUB:
		err = e.runGOSUB()
		e.jump = true
	case token.GOTO:
		err = e.runGOTO()
		e.jump = true
	case token.INPUT:
		err = e.runINPUT()
	case token.IF:
		err = e.runIF()
	case token.LET:
		err = e.runLET()
	case token.NEXT:
		err = e.runNEXT()
	case token.PRINT:
		err = e.runPRINT()
	case token.REM:
		err = e.runREM()
	case token.RETURN:
		err = e.runRETURN()
	case token.BUILTIN:

		obj := e.callBuiltin(tok.Literal)

		if obj.Type() == object.ERROR {
			return fmt.Errorf("%s", obj.(*object.ErrorObject).Value)
		}

		e.offset--
	default:
		err = fmt.Errorf("Token not handled: %v", tok)
	}

	//
	// Ready for the next instruction
	//
	e.offset++

	//
	// Error?
	//
	if err != nil {
		return err
	}
	return nil
}

// Run launches the program, and does not return until it is over.
//
// A program will terminate when the control reaches the end of the
// final-line, or when the "END" token is encountered.
func (e *Interpreter) Run() error {

	//
	// We walk our series of tokens.
	//
	for e.offset < len(e.program) && !e.finished {

		err := e.RunOnce()

		if err != nil {

			return fmt.Errorf("Line %s : %s", e.lineno, err.Error())
			return err
		}
	}

	//
	// Here we've finished with no error, but we want to
	// alert on unclosed FOR-loops.
	//
	if !e.loops.Empty() {
		return fmt.Errorf("Unclosed FOR loop")
	}

	return nil
}

// SetVariable sets the contents of a variable in the interpreter environment.
//
// Useful for testing/embedding.
//
func (e *Interpreter) SetVariable(id string, val object.Object) {
	e.vars.Set(id, val)
}

// GetVariable returns the contents of the given variable.
//
// Useful for testing/embedding.
//
func (e *Interpreter) GetVariable(id string) object.Object {

	val := e.vars.Get(id)
	if val != nil {
		return val
	}
	return object.Error("The variable '%s' doesn't exist", id)
}

// RegisterBuiltin registers a function as a built-in, so that it can
// be called from the users' BASIC program.
//
// Useful for embedding.
//
func (e *Interpreter) RegisterBuiltin(name string, nArgs int, ft BuiltinSig) {

	// Register the built-in
	e.functions.Register(name, nArgs, ft)

	// Now ensure that in the future if we hit this built-in
	// we regard it as a function-call, not a variable
	for i := 0; i < len(e.program); i++ {

		// Is this token a reference to the function
		// as an ident?
		if e.program[i].Type == token.IDENT &&
			e.program[i].Literal == name {

			// Change the type.  (Hack!)
			e.program[i].Type = token.BUILTIN
		}
	}
}
