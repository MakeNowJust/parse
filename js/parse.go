package js

import (
	"fmt"
	"io"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/buffer"
)

// Parser is the state for the parser.
type Parser struct {
	l   *Lexer
	err error

	tt               TokenType
	data             []byte
	prevLT           bool
	inFor            bool
	async, generator bool
	assumeArrowFunc  bool

	ast   *AST
	scope *Scope
}

// Parse returns a JS AST tree of.
func Parse(r *parse.Input) (*AST, error) {
	p := &Parser{
		l:   NewLexer(r),
		tt:  WhitespaceToken, // trick so that next() works
		ast: newAST(),
	}

	p.tt, p.data = p.l.Next()
	if p.tt == CommentToken || p.tt == CommentLineTerminatorToken {
		p.ast.Comment = p.data
		p.next()
	}
	p.ast.Module = p.parseModule()

	if p.err == nil {
		p.err = p.l.Err()
	} else {
		offset := p.l.r.Offset() - len(p.data)
		p.err = parse.NewError(buffer.NewReader(p.l.r.Bytes()), offset, p.err.Error())
	}
	if p.err == io.EOF {
		p.err = nil
	}
	return p.ast, p.err
}

////////////////////////////////////////////////////////////////

func (p *Parser) next() {
	p.prevLT = false
	p.tt, p.data = p.l.Next()
	for p.tt == WhitespaceToken || p.tt == LineTerminatorToken || p.tt == CommentToken || p.tt == CommentLineTerminatorToken {
		if p.tt == LineTerminatorToken || p.tt == CommentLineTerminatorToken {
			p.prevLT = true
		}
		p.tt, p.data = p.l.Next()
	}
}

func (p *Parser) failMessage(msg string, args ...interface{}) {
	if p.err == nil {
		p.err = fmt.Errorf(msg, args...)
		p.tt = ErrorToken
	}
}

func (p *Parser) fail(in string, expected ...TokenType) {
	if p.err == nil {
		msg := "unexpected"
		if 0 < len(expected) {
			msg = "expected"
			for i, tt := range expected[:len(expected)-1] {
				if 0 < i {
					msg += ","
				}
				msg += " '" + tt.String() + "'"
			}
			if 2 < len(expected) {
				msg += ", or"
			} else if 1 < len(expected) {
				msg += " or"
			}
			msg += " '" + expected[len(expected)-1].String() + "' instead of"
		}

		if p.tt == ErrorToken {
			if p.l.Err() == io.EOF {
				msg += " EOF"
			} else if lexerErr, ok := p.l.Err().(*parse.Error); ok {
				msg = lexerErr.Message
			} else {
				// does not happen
			}
		} else {
			msg += " '" + string(p.data) + "'"
		}
		if in != "" {
			msg += " in " + in
		}

		p.err = fmt.Errorf(msg)
		p.tt = ErrorToken
	}
}

func (p *Parser) consume(in string, tt TokenType) bool {
	if p.tt != tt {
		p.fail(in, tt)
		return false
	}
	p.next()
	return true
}

func (p *Parser) enterScope(scope *Scope, isFunc bool) *Scope {
	// create a new scope object and add it to the parent
	parent := p.scope
	p.scope = scope
	*scope = Scope{parent, nil, VarArray{}, VarArray{}, 0}
	if isFunc {
		scope.Func = scope
	} else if parent != nil {
		scope.Func = parent.Func
	}
	return parent
}

func (p *Parser) exitScope(parent *Scope) {
	p.scope.HoistUndeclared(p.ast)
	p.scope = parent
}

func (p *Parser) parseModule() (module Module) {
	p.enterScope(&module.Scope, true)
	for {
		switch p.tt {
		case ErrorToken:
			return
		case ImportToken:
			importStmt := p.parseImportStmt()
			module.List = append(module.List, &importStmt)
		case ExportToken:
			exportStmt := p.parseExportStmt()
			module.List = append(module.List, &exportStmt)
		default:
			module.List = append(module.List, p.parseStmt(true))
		}
	}
}

func (p *Parser) parseStmt(allowDeclaration bool) (stmt IStmt) {
	switch tt := p.tt; tt {
	case OpenBraceToken:
		blockStmt := p.parseBlockStmt("block statement", true)
		stmt = &blockStmt
	case ConstToken, VarToken:
		if !allowDeclaration && tt == ConstToken {
			p.fail("statement")
			return
		}
		p.next()
		varDecl := p.parseVarDecl(tt)
		stmt = &varDecl
		if !p.prevLT && p.tt != SemicolonToken && p.tt != CloseBraceToken && p.tt != ErrorToken {
			if tt == ConstToken {
				p.fail("const declaration")
			} else {
				p.fail("var statement")
			}
			return
		}
	case LetToken:
		let := p.data
		p.next()
		if allowDeclaration && (IsIdentifier(p.tt) || p.tt == YieldToken || p.tt == AwaitToken || p.tt == OpenBracketToken || p.tt == OpenBraceToken) {
			varDecl := p.parseVarDecl(tt)
			stmt = &varDecl
			if !p.prevLT && p.tt != SemicolonToken && p.tt != CloseBraceToken && p.tt != ErrorToken {
				p.fail("let declaration")
				return
			}
		} else {
			// expression
			stmt = &ExprStmt{p.parseIdentifierExpression(OpExpr, let)}
			if !p.prevLT && p.tt != SemicolonToken && p.tt != CloseBraceToken && p.tt != ErrorToken {
				p.fail("expression")
				return
			}
		}
	case IfToken:
		p.next()
		if !p.consume("if statement", OpenParenToken) {
			return
		}
		cond := p.parseExpression(OpExpr)
		if !p.consume("if statement", CloseParenToken) {
			return
		}
		body := p.parseStmt(false)

		var elseBody IStmt
		if p.tt == ElseToken {
			p.next()
			elseBody = p.parseStmt(false)
		}
		stmt = &IfStmt{cond, body, elseBody}
	case ContinueToken, BreakToken:
		tt := p.tt
		p.next()
		var label []byte
		if !p.prevLT && p.isIdentifierReference(p.tt) {
			label = p.data
			p.next()
		}
		stmt = &BranchStmt{tt, label}
	case ReturnToken:
		p.next()
		var value IExpr
		if !p.prevLT && p.tt != SemicolonToken && p.tt != CloseBraceToken && p.tt != ErrorToken {
			value = p.parseExpression(OpExpr)
		}
		stmt = &ReturnStmt{value}
	case WithToken:
		p.next()
		if !p.consume("with statement", OpenParenToken) {
			return
		}
		cond := p.parseExpression(OpExpr)
		if !p.consume("with statement", CloseParenToken) {
			return
		}
		stmt = &WithStmt{cond, p.parseStmt(false)}
	case DoToken:
		stmt = &DoWhileStmt{}
		p.next()
		body := p.parseStmt(false)
		if !p.consume("do-while statement", WhileToken) {
			return
		}
		if !p.consume("do-while statement", OpenParenToken) {
			return
		}
		stmt = &DoWhileStmt{p.parseExpression(OpExpr), body}
		if !p.consume("do-while statement", CloseParenToken) {
			return
		}
	case WhileToken:
		p.next()
		if !p.consume("while statement", OpenParenToken) {
			return
		}
		cond := p.parseExpression(OpExpr)
		if !p.consume("while statement", CloseParenToken) {
			return
		}
		stmt = &WhileStmt{cond, p.parseStmt(false)}
	case ForToken:
		p.next()
		await := p.async && p.tt == AwaitToken
		if await {
			p.next()
		}
		if !p.consume("for statement", OpenParenToken) {
			return
		}

		var init IExpr
		p.inFor = true
		if p.tt == VarToken || p.tt == LetToken || p.tt == ConstToken {
			tt := p.tt
			p.next()
			varDecl := p.parseVarDecl(tt)
			init = &varDecl
		} else if p.tt != SemicolonToken {
			init = p.parseExpression(OpExpr)
		}
		p.inFor = false

		if p.tt == SemicolonToken {
			var cond, post IExpr
			if await {
				p.fail("for statement", OfToken)
				return
			}
			p.next()
			if p.tt != SemicolonToken {
				cond = p.parseExpression(OpExpr)
			}
			if !p.consume("for statement", SemicolonToken) {
				return
			}
			if p.tt != CloseParenToken {
				post = p.parseExpression(OpExpr)
			}
			if !p.consume("for statement", CloseParenToken) {
				return
			}
			stmt = &ForStmt{init, cond, post, p.parseStmt(false)}
		} else if p.tt == InToken {
			if await {
				p.fail("for statement", OfToken)
				return
			}
			p.next()
			value := p.parseExpression(OpExpr)
			if !p.consume("for statement", CloseParenToken) {
				return
			}
			stmt = &ForInStmt{init, value, p.parseStmt(false)}
		} else if p.tt == OfToken {
			p.next()
			value := p.parseExpression(OpAssign)
			if !p.consume("for statement", CloseParenToken) {
				return
			}
			stmt = &ForOfStmt{await, init, value, p.parseStmt(false)}
		} else {
			p.fail("for statement", InToken, OfToken, SemicolonToken)
			return
		}
	case SwitchToken:
		p.next()
		if !p.consume("switch statement", OpenParenToken) {
			return
		}
		init := p.parseExpression(OpExpr)
		if !p.consume("switch statement", CloseParenToken) {
			return
		}

		// case block
		if !p.consume("switch statement", OpenBraceToken) {
			return
		}

		clauses := []CaseClause{}
		for {
			if p.tt == ErrorToken {
				p.fail("switch statement")
				return
			} else if p.tt == CloseBraceToken {
				p.next()
				break
			}

			clause := p.tt
			var list IExpr
			if p.tt == CaseToken {
				p.next()
				list = p.parseExpression(OpExpr)
			} else if p.tt == DefaultToken {
				p.next()
			} else {
				p.fail("switch statement", CaseToken, DefaultToken)
				return
			}
			if !p.consume("switch statement", ColonToken) {
				return
			}

			var stmts []IStmt
			for p.tt != CaseToken && p.tt != DefaultToken && p.tt != CloseBraceToken && p.tt != ErrorToken {
				stmts = append(stmts, p.parseStmt(true))
			}
			clauses = append(clauses, CaseClause{clause, list, stmts})
		}
		stmt = &SwitchStmt{init, clauses}
	case FunctionToken:
		if !allowDeclaration {
			p.fail("statement")
			return
		}
		funcDecl := p.parseFuncDecl()
		stmt = &funcDecl
	case AsyncToken: // async function
		if !allowDeclaration {
			p.fail("statement")
			return
		}
		async := p.data
		p.next()
		if p.tt == FunctionToken && !p.prevLT {
			funcDecl := p.parseAsyncFuncDecl()
			stmt = &funcDecl
		} else {
			// expression
			stmt = &ExprStmt{p.parseAsyncExpression(OpExpr, async)}
			if !p.prevLT && p.tt != SemicolonToken && p.tt != CloseBraceToken && p.tt != ErrorToken {
				p.fail("expression")
				return
			}
		}
	case ClassToken:
		if !allowDeclaration {
			p.fail("statement")
			return
		}
		classDecl := p.parseClassDecl()
		stmt = &classDecl
	case ThrowToken:
		p.next()
		var value IExpr
		if !p.prevLT {
			value = p.parseExpression(OpExpr)
		}
		stmt = &ThrowStmt{value}
	case TryToken:
		p.next()
		body := p.parseBlockStmt("try statement", true)
		var binding IBinding
		var catch, finally BlockStmt
		if p.tt == CatchToken {
			p.next()

			parent := p.enterScope(&catch.Scope, false)
			if p.tt == OpenParenToken {
				p.next()
				binding = p.parseBinding(LexicalDecl) // local to block scope of catch
				if !p.consume("try-catch statement", CloseParenToken) {
					return
				}
			}
			block := p.parseBlockStmt("try-catch statement", false)
			catch.List = block.List
			p.exitScope(parent)
		}
		if p.tt == FinallyToken {
			p.next()
			finally = p.parseBlockStmt("try-finally statement", true)
		}
		stmt = &TryStmt{body, binding, catch, finally}
	case DebuggerToken:
		p.next()
		stmt = &DebuggerStmt{}
	case SemicolonToken, ErrorToken:
		stmt = &EmptyStmt{}
	default:
		if p.isIdentifierReference(p.tt) {
			// labelled statement or expression
			label := p.data
			p.next()
			if p.tt == ColonToken {
				p.next()
				stmt = &LabelledStmt{label, p.parseStmt(true)} // allows illegal async function, generator function, let, const, or class declarations
			} else {
				// expression
				stmt = &ExprStmt{p.parseIdentifierExpression(OpExpr, label)}
				if !p.prevLT && p.tt != SemicolonToken && p.tt != CloseBraceToken && p.tt != ErrorToken {
					p.fail("expression")
					return
				}
			}
		} else {
			// expression
			stmt = &ExprStmt{p.parseExpression(OpExpr)}
			if !p.prevLT && p.tt != SemicolonToken && p.tt != CloseBraceToken && p.tt != ErrorToken {
				p.fail("expression")
				return
			}
		}
	}
	if p.tt == SemicolonToken {
		p.next()
	}
	return
}

func (p *Parser) parseBlockStmt(in string, enterContext bool) (blockStmt BlockStmt) {
	var parent *Scope
	if enterContext {
		parent = p.enterScope(&blockStmt.Scope, false)
	}
	if !p.consume(in, OpenBraceToken) {
		return
	}
	for {
		if p.tt == ErrorToken {
			p.fail("")
			return
		} else if p.tt == CloseBraceToken {
			p.next()
			break
		}
		blockStmt.List = append(blockStmt.List, p.parseStmt(true))
	}
	if enterContext {
		p.exitScope(parent)
	}
	return
}

func (p *Parser) parseImportStmt() (importStmt ImportStmt) {
	// assume we're at import
	p.next()
	if p.tt == StringToken {
		importStmt.Module = p.data
		p.next()
	} else {
		if IsIdentifier(p.tt) || p.tt == YieldToken || p.tt == AwaitToken {
			importStmt.Default = p.data
			p.next()
			if p.tt == CommaToken {
				p.next()
			}
		}
		if p.tt == MulToken {
			star := p.data
			p.next()
			if !p.consume("import statement", AsToken) {
				return
			}
			if !IsIdentifier(p.tt) && p.tt != YieldToken && p.tt != AwaitToken {
				p.fail("import statement", IdentifierToken)
				return
			}
			importStmt.List = []Alias{Alias{star, p.data}}
			p.next()
		} else if p.tt == OpenBraceToken {
			p.next()
			for IsIdentifierName(p.tt) {
				var name, binding []byte = nil, p.data
				p.next()
				if p.tt == AsToken {
					p.next()
					if !IsIdentifier(p.tt) && p.tt != YieldToken && p.tt != AwaitToken {
						p.fail("import statement", IdentifierToken)
						return
					}
					name = binding
					binding = p.data
					p.next()
				}
				importStmt.List = append(importStmt.List, Alias{name, binding})
				if p.tt == CommaToken {
					p.next()
					if p.tt == CloseBraceToken {
						importStmt.List = append(importStmt.List, Alias{})
						break
					}
				}
			}
			if !p.consume("import statement", CloseBraceToken) {
				return
			}
		}
		if importStmt.Default == nil && len(importStmt.List) == 0 {
			p.fail("import statement", StringToken, IdentifierToken, MulToken, OpenBraceToken)
			return
		}

		if !p.consume("import statement", FromToken) {
			return
		}
		if p.tt != StringToken {
			p.fail("import statement", StringToken)
			return
		}
		importStmt.Module = p.data
		p.next()
	}
	if p.tt == SemicolonToken {
		p.next()
	}
	return
}

func (p *Parser) parseExportStmt() (exportStmt ExportStmt) {
	// assume we're at export
	p.next()
	if p.tt == MulToken || p.tt == OpenBraceToken {
		if p.tt == MulToken {
			star := p.data
			p.next()
			if p.tt == AsToken {
				p.next()
				if !IsIdentifierName(p.tt) {
					p.fail("export statement", IdentifierToken)
					return
				}
				exportStmt.List = []Alias{Alias{star, p.data}}
				p.next()
			} else {
				exportStmt.List = []Alias{Alias{nil, star}}
			}
			if p.tt != FromToken {
				p.fail("export statement", FromToken)
				return
			}
		} else {
			p.next()
			for IsIdentifierName(p.tt) {
				var name, binding []byte = nil, p.data
				p.next()
				if p.tt == AsToken {
					p.next()
					if !IsIdentifierName(p.tt) {
						p.fail("export statement", IdentifierToken)
						return
					}
					name = binding
					binding = p.data
					p.next()
				}
				exportStmt.List = append(exportStmt.List, Alias{name, binding})
				if p.tt == CommaToken {
					p.next()
					if p.tt == CloseBraceToken {
						exportStmt.List = append(exportStmt.List, Alias{})
						break
					}
				}
			}
			if !p.consume("export statement", CloseBraceToken) {
				return
			}
		}
		if p.tt == FromToken {
			p.next()
			if p.tt != StringToken {
				p.fail("export statement", StringToken)
				return
			}
			exportStmt.Module = p.data
			p.next()
		}
	} else if p.tt == VarToken || p.tt == ConstToken || p.tt == LetToken {
		tt := p.tt
		p.next()
		varDecl := p.parseVarDecl(tt)
		exportStmt.Decl = &varDecl
	} else if p.tt == FunctionToken {
		funcDecl := p.parseFuncDecl()
		exportStmt.Decl = &funcDecl
	} else if p.tt == AsyncToken { // async function
		p.next()
		if p.tt != FunctionToken || p.prevLT {
			p.fail("export statement", FunctionToken)
			return
		}
		funcDecl := p.parseAsyncFuncDecl()
		exportStmt.Decl = &funcDecl
	} else if p.tt == ClassToken {
		classDecl := p.parseClassDecl()
		exportStmt.Decl = &classDecl
	} else if p.tt == DefaultToken {
		exportStmt.Default = true
		p.next()
		if p.tt == FunctionToken {
			funcDecl := p.parseFuncExpr()
			exportStmt.Decl = &funcDecl
		} else if p.tt == AsyncToken { // async function or async arrow function
			async := p.data
			p.next()
			if p.tt == FunctionToken && !p.prevLT {
				funcDecl := p.parseAsyncFuncDecl()
				exportStmt.Decl = &funcDecl
			} else {
				// expression
				exportStmt.Decl = p.parseAsyncExpression(OpExpr, async)
			}
		} else if p.tt == ClassToken {
			classDecl := p.parseClassDecl()
			exportStmt.Decl = &classDecl
		} else {
			exportStmt.Decl = p.parseExpression(OpAssign)
		}
	} else {
		p.fail("export statement", MulToken, OpenBraceToken, VarToken, LetToken, ConstToken, FunctionToken, AsyncToken, ClassToken, DefaultToken)
		return
	}
	if p.tt == SemicolonToken {
		p.next()
	}
	return
}

func (p *Parser) parseVarDecl(tt TokenType) (varDecl VarDecl) {
	// assume we're past var, let or const
	varDecl.TokenType = tt
	declType := LexicalDecl
	if tt == VarToken {
		declType = VariableDecl
	}
	for {
		varDecl.List = append(varDecl.List, p.parseBindingElement(declType))
		if p.tt == CommaToken {
			p.next()
		} else {
			break
		}
	}
	return
}

func (p *Parser) parseFuncParams(in string) (params Params) {
	if !p.consume(in, OpenParenToken) {
		return
	}

	for p.tt != CloseParenToken && p.tt != ErrorToken {
		if p.tt == EllipsisToken {
			// binding rest element
			p.next()
			params.Rest = p.parseBinding(ArgumentDecl)
			p.consume(in, CloseParenToken)
			return
		}
		params.List = append(params.List, p.parseBindingElement(ArgumentDecl))
		if p.tt != CommaToken {
			break
		}
		p.next()
	}
	if p.tt != CloseParenToken {
		p.fail(in)
		return
	}
	p.next()

	// mark undeclared vars as arguments in `function f(a=b){var b}` where the b's are different vars
	p.scope.MarkArguments()
	return
}

func (p *Parser) parseFuncDecl() (funcDecl FuncDecl) {
	return p.parseAnyFunc(false, false)
}

func (p *Parser) parseAsyncFuncDecl() (funcDecl FuncDecl) {
	return p.parseAnyFunc(true, false)
}

func (p *Parser) parseFuncExpr() (funcDecl FuncDecl) {
	return p.parseAnyFunc(false, true)
}

func (p *Parser) parseAsyncFuncExpr() (funcDecl FuncDecl) {
	return p.parseAnyFunc(true, true)
}

func (p *Parser) parseAnyFunc(async, inExpr bool) (funcDecl FuncDecl) {
	// assume we're at function
	p.next()
	funcDecl.Async = async
	funcDecl.Generator = p.tt == MulToken
	if funcDecl.Generator {
		p.next()
	}
	var ok bool
	var name []byte
	if inExpr && (IsIdentifier(p.tt) || p.tt == YieldToken || p.tt == AwaitToken) || !inExpr && p.isIdentifierReference(p.tt) {
		name = p.data
		if !inExpr {
			funcDecl.Name, ok = p.scope.Declare(p.ast, FunctionDecl, p.data)
			if !ok {
				p.failMessage("identifier '%s' has already been declared", string(p.data))
				return
			}
		}
		p.next()
	} else if p.tt != OpenParenToken {
		p.fail("function declaration", IdentifierToken, OpenParenToken)
		return
	}
	parent := p.enterScope(&funcDecl.Scope, true)
	parentAsync, parentGenerator := p.async, p.generator
	p.async, p.generator = funcDecl.Async, funcDecl.Generator

	if inExpr && name != nil {
		funcDecl.Name, _ = p.scope.Declare(p.ast, ExprDecl, name) // cannot fail
	}
	funcDecl.Params = p.parseFuncParams("function declaration")
	funcDecl.Body = p.parseBlockStmt("function declaration", false)

	p.async, p.generator = parentAsync, parentGenerator
	p.exitScope(parent)
	return
}

func (p *Parser) parseClassDecl() (classDecl ClassDecl) {
	return p.parseAnyClass(false)
}

func (p *Parser) parseClassExpr() (classDecl ClassDecl) {
	return p.parseAnyClass(true)
}

func (p *Parser) parseAnyClass(inExpr bool) (classDecl ClassDecl) {
	// assume we're at class
	p.next()
	if IsIdentifier(p.tt) || p.tt == YieldToken || p.tt == AwaitToken {
		if !inExpr {
			var ok bool
			classDecl.Name, ok = p.scope.Declare(p.ast, LexicalDecl, p.data)
			if !ok {
				p.failMessage("identifier '%s' has already been declared", string(p.data))
				return
			}
		} else {
			//classDecl.Name, ok = p.scope.Declare(p.ast, ExprDecl, p.data) // classes do not register vars
			v := p.ast.AddVar(ExprDecl, p.data)
			classDecl.Name = v.Ref
		}
		p.next()
	}
	if p.tt == ExtendsToken {
		p.next()
		classDecl.Extends = p.parseExpression(OpLHS)
	}

	if !p.consume("class declaration", OpenBraceToken) {
		return
	}
	for {
		if p.tt == ErrorToken {
			p.fail("class declaration")
			return
		} else if p.tt == SemicolonToken {
			p.next()
			continue
		} else if p.tt == CloseBraceToken {
			p.next()
			break
		}
		classDecl.Methods = append(classDecl.Methods, p.parseMethod())
	}
	return
}

func (p *Parser) parseMethod() (method MethodDecl) {
	var data []byte
	if p.tt == StaticToken {
		method.Static = true
		data = p.data
		p.next()
	}
	if p.tt == MulToken {
		method.Generator = true
		p.next()
	} else if p.tt == AsyncToken {
		data = p.data
		p.next()
		if !p.prevLT {
			method.Async = true
			if p.tt == MulToken {
				method.Generator = true
				data = nil
				p.next()
			}
		}
	} else if p.tt == GetToken {
		method.Get = true
		data = p.data
		p.next()
	} else if p.tt == SetToken {
		method.Set = true
		data = p.data
		p.next()
	}

	if data != nil && p.tt == OpenParenToken {
		method.Name.Literal = LiteralExpr{IdentifierToken, data}
		if method.Async || method.Get || method.Set {
			method.Async = false
			method.Get = false
			method.Set = false
		} else {
			method.Static = false
		}
	} else {
		method.Name = p.parsePropertyName("method definition")
	}

	parent := p.enterScope(&method.Scope, true)
	parentAsync, parentGenerator := p.async, p.generator
	p.async, p.generator = method.Async, method.Generator

	method.Params = p.parseFuncParams("method definition")
	method.Body = p.parseBlockStmt("method definition", false)

	p.async, p.generator = parentAsync, parentGenerator
	p.exitScope(parent)
	return
}

func (p *Parser) parsePropertyName(in string) (propertyName PropertyName) {
	if IsIdentifierName(p.tt) {
		propertyName.Literal = LiteralExpr{IdentifierToken, p.data}
		p.next()
	} else if p.tt == StringToken {
		if _, ok := ParseIdentifierName(p.data[1 : len(p.data)-1]); ok {
			propertyName.Literal = LiteralExpr{IdentifierToken, p.data[1 : len(p.data)-1]}
		} else if tt, ok := ParseNumericLiteral(p.data[1 : len(p.data)-1]); ok {
			propertyName.Literal = LiteralExpr{tt, p.data[1 : len(p.data)-1]}
		} else {
			propertyName.Literal = LiteralExpr{p.tt, p.data}
		}
		p.next()
	} else if IsNumeric(p.tt) {
		propertyName.Literal = LiteralExpr{p.tt, p.data}
		p.next()
	} else if p.tt == OpenBracketToken {
		p.next()
		propertyName.Computed = p.parseExpression(OpAssign)
		if !p.consume(in, CloseBracketToken) {
			return
		}
	} else {
		p.fail(in, IdentifierToken, StringToken, NumericToken, OpenBracketToken)
		return
	}
	return
}

func (p *Parser) parseBindingElement(decl DeclType) (bindingElement BindingElement) {
	// binding element
	bindingElement.Binding = p.parseBinding(decl)
	if p.tt == EqToken {
		p.next()
		bindingElement.Default = p.parseExpression(OpAssign)
	}
	return
}

func (p *Parser) parseBinding(decl DeclType) (binding IBinding) {
	// binding identifier or binding pattern
	if IsIdentifier(p.tt) || !p.generator && p.tt == YieldToken || !p.async && p.tt == AwaitToken {
		var ok bool
		binding, ok = p.scope.Declare(p.ast, decl, p.data)
		if !ok {
			p.failMessage("identifier '%s' has already been declared", string(p.data))
			return
		}
		p.next()
	} else if p.tt == OpenBracketToken {
		p.next()
		array := BindingArray{}
		if p.tt == CommaToken {
			array.List = append(array.List, BindingElement{})
		}
		for p.tt != CloseBracketToken {
			// elision
			for p.tt == CommaToken {
				p.next()
				if p.tt == CommaToken {
					array.List = append(array.List, BindingElement{})
				}
			}
			// binding rest element
			if p.tt == EllipsisToken {
				p.next()
				array.Rest = p.parseBinding(decl)
				if p.tt != CloseBracketToken {
					p.fail("array binding pattern", CloseBracketToken)
					return
				}
				break
			} else if p.tt == CloseBracketToken {
				break
			}

			array.List = append(array.List, p.parseBindingElement(decl))

			if p.tt != CommaToken && p.tt != CloseBracketToken {
				p.fail("array binding pattern", CommaToken, CloseBracketToken)
				return
			}
		}
		p.next() // always CloseBracketToken
		binding = &array
	} else if p.tt == OpenBraceToken {
		p.next()
		object := BindingObject{}
		for p.tt != CloseBraceToken {
			// binding rest property
			if p.tt == EllipsisToken {
				p.next()
				if !p.isIdentifierReference(p.tt) {
					p.fail("object binding pattern", IdentifierToken)
					return
				}
				var ok bool
				object.Rest, ok = p.scope.Declare(p.ast, decl, p.data)
				if !ok {
					p.failMessage("identifier '%s' has already been declared", string(p.data))
					return
				}
				p.next()
				if p.tt != CloseBraceToken {
					p.fail("object binding pattern", CloseBraceToken)
					return
				}
				break
			}

			item := BindingObjectItem{}
			if p.isIdentifierReference(p.tt) {
				name := p.data
				item.Key = PropertyName{LiteralExpr{IdentifierToken, p.data}, nil}
				p.next()
				if p.tt == ColonToken {
					// property name + : + binding element
					p.next()
					item.Value = p.parseBindingElement(decl)
				} else {
					// single name binding
					var ok bool
					item.Key.Literal.Data = parse.Copy(item.Key.Literal.Data) // copy so that renaming doesn't rename the key
					item.Value.Binding, ok = p.scope.Declare(p.ast, decl, name)
					if !ok {
						p.failMessage("identifier '%s' has already been declared", string(name))
						return
					}
					if p.tt == EqToken {
						p.next()
						item.Value.Default = p.parseExpression(OpAssign)
					}
				}
			} else {
				propertyName := p.parsePropertyName("object binding pattern")
				item.Key = propertyName
				if !p.consume("object binding pattern", ColonToken) {
					return
				}
				item.Value = p.parseBindingElement(decl)
			}
			object.List = append(object.List, item)

			if p.tt == CommaToken {
				p.next()
			} else if p.tt != CloseBraceToken {
				p.fail("object binding pattern", CommaToken, CloseBraceToken)
				return
			}
		}
		p.next() // always CloseBracketToken
		binding = &object
	} else {
		p.fail("binding")
		return
	}
	return
}

func (p *Parser) parseArrayLiteral() (array ArrayExpr) {
	// assume we're on [
	p.next()
	prevComma := true
	for {
		if p.tt == ErrorToken {
			p.fail("expression")
			return
		} else if p.tt == CloseBracketToken {
			p.next()
			break
		} else if p.tt == CommaToken {
			if prevComma {
				array.List = append(array.List, Element{})
			}
			prevComma = true
			p.next()
		} else {
			spread := p.tt == EllipsisToken
			if spread {
				p.next()
			}
			array.List = append(array.List, Element{p.parseAssignmentExpression(), spread})
			prevComma = false
			if spread && p.tt != CloseBracketToken {
				p.assumeArrowFunc = false
			}
		}
	}
	return
}

func (p *Parser) parseObjectLiteral() (object ObjectExpr) {
	// assume we're on {
	p.next()
	for {
		if p.tt == ErrorToken {
			p.fail("object literal", CloseBraceToken)
			return
		} else if p.tt == CloseBraceToken {
			p.next()
			break
		}

		property := Property{}
		if p.tt == EllipsisToken {
			p.next()
			property.Spread = true
			property.Value = p.parseAssignmentExpression()
			if p.tt != CloseBraceToken {
				p.assumeArrowFunc = false
			}
		} else {
			// try to parse as MethodDefinition, otherwise fall back to PropertyName:AssignExpr or IdentifierReference
			var data []byte
			method := MethodDecl{}
			if p.tt == MulToken {
				p.next()
				method.Generator = true
			} else if p.tt == AsyncToken {
				data = p.data
				p.next()
				if !p.prevLT {
					method.Async = true
					if p.tt == MulToken {
						p.next()
						method.Generator = true
						data = nil
					}
				} else {
					method.Name.Literal = LiteralExpr{IdentifierToken, data}
					data = nil
				}
			} else if p.tt == GetToken {
				data = p.data
				p.next()
				method.Get = true
			} else if p.tt == SetToken {
				data = p.data
				p.next()
				method.Set = true
			}

			// PropertyName
			if data != nil && !method.Generator && (p.tt == EqToken || p.tt == CommaToken || p.tt == CloseBraceToken || p.tt == ColonToken || p.tt == OpenParenToken) {
				method.Name.Literal = LiteralExpr{IdentifierToken, data}
				method.Async = false
				method.Get = false
				method.Set = false
			} else if !method.Name.IsSet() { // did not parse async [LT]
				method.Name = p.parsePropertyName("object literal")
				if !method.Name.IsSet() {
					return
				}
			}

			if p.tt == OpenParenToken {
				// MethodDefinition
				parent := p.enterScope(&method.Scope, true)
				parentAsync, parentGenerator := p.async, p.generator
				p.async, p.generator = method.Async, method.Generator

				method.Params = p.parseFuncParams("method definition")
				method.Body = p.parseBlockStmt("method definition", false)

				p.async, p.generator = parentAsync, parentGenerator
				p.exitScope(parent)
				property.Value = &method
				p.assumeArrowFunc = false
			} else if p.tt == ColonToken {
				// PropertyName : AssignmentExpression
				p.next()
				property.Name = method.Name
				property.Value = p.parseAssignmentExpression()
			} else if method.Name.IsComputed() || !p.isIdentifierReference(method.Name.Literal.TokenType) {
				p.fail("object literal", ColonToken, OpenParenToken)
				return
			} else {
				// IdentifierReference (= AssignmentExpression)?
				name := method.Name.Literal.Data
				method.Name.Literal.Data = parse.Copy(method.Name.Literal.Data) // copy so that renaming doesn't rename the key
				property.Name = method.Name                                     // set key explicitly so after renaming the original is still known
				if p.assumeArrowFunc {
					property.Value, _ = p.scope.Declare(p.ast, ArgumentDecl, name) // cannot fail
				} else {
					property.Value = p.scope.Use(p.ast, name)
				}
				if p.tt == EqToken {
					p.next()
					property.Init = p.parseExpression(OpAssign)
				}
			}
		}
		object.List = append(object.List, property)
		if p.tt == CommaToken {
			p.next()
		} else if p.tt != CloseBraceToken {
			p.fail("object literal")
			return
		}
	}
	return
}

func (p *Parser) parseTemplateLiteral() (template TemplateExpr) {
	// assume we're on 'Template' or 'TemplateStart'
	for p.tt == TemplateStartToken || p.tt == TemplateMiddleToken {
		tpl := p.data
		p.next()
		template.List = append(template.List, TemplatePart{tpl, p.parseExpression(OpExpr)})
		if p.tt == TemplateEndToken {
			break
		} else {
			p.fail("template literal", TemplateToken)
			return
		}
	}
	template.Tail = p.data
	p.next() // TemplateEndToken
	return
}

func (p *Parser) parseArgs() (args Arguments) {
	// assume we're on (
	p.next()
	args.List = make([]IExpr, 0, 4)
	for {
		if p.tt == EllipsisToken {
			p.next()
			args.Rest = p.parseExpression(OpAssign)
			if p.tt == CommaToken {
				p.next()
			}
			break
		}

		if p.tt == CloseParenToken || p.tt == ErrorToken {
			break
		}
		args.List = append(args.List, p.parseExpression(OpAssign))
		if p.tt == CommaToken {
			p.next()
		}
	}
	p.consume("arguments", CloseParenToken)
	return
}

func (p *Parser) parseAsyncArrowFunc() (arrowFunc ArrowFunc) {
	// expect we're at Identifier or Yield or (
	parent := p.enterScope(&arrowFunc.Scope, true)
	parentAsync, parentGenerator := p.async, p.generator
	p.async, p.generator = true, false

	if IsIdentifier(p.tt) || !p.generator && p.tt == YieldToken {
		ref, _ := p.scope.Declare(p.ast, ArgumentDecl, p.data)
		p.next()
		arrowFunc.Params.List = []BindingElement{{Binding: ref}}
	} else {
		arrowFunc.Params = p.parseFuncParams("arrow function")
	}

	arrowFunc.Async = true
	arrowFunc.Body = p.parseArrowFuncBody()

	p.async, p.generator = parentAsync, parentGenerator
	p.exitScope(parent)
	return
}

func (p *Parser) parseIdentifierArrowFunc(ref VarRef) (arrowFunc ArrowFunc) {
	// expect we're at =>
	parent := p.enterScope(&arrowFunc.Scope, true)
	parentAsync, parentGenerator := p.async, p.generator
	p.async, p.generator = false, false

	v := p.ast.Vars[ref]
	if 1 < v.Uses {
		v.Uses--
		ref, _ = p.scope.Declare(p.ast, ArgumentDecl, v.Name) // cannot fail
	} else {
		// must be undeclared
		p.scope.Parent.Undeclared = p.scope.Parent.Undeclared[:len(p.scope.Parent.Undeclared)-1]
		v.Decl = ArgumentDecl
		p.scope.Declared = append(p.scope.Declared, v)
	}

	arrowFunc.Params.List = []BindingElement{{ref, nil}}
	arrowFunc.Body = p.parseArrowFuncBody()

	p.async, p.generator = parentAsync, parentGenerator
	p.exitScope(parent)
	return
}

func (p *Parser) parseArrowFuncBody() (body BlockStmt) {
	// expect we're at arrow
	if p.tt != ArrowToken {
		p.fail("arrow function", ArrowToken)
		return
	} else if p.prevLT {
		p.fail("expression")
		return
	}
	p.next()

	// mark undeclared vars as arguments in `function f(a=b){var b}` where the b's are different vars
	p.scope.MarkArguments()

	if p.tt == OpenBraceToken {
		body = p.parseBlockStmt("arrow function", false)
	} else {
		body.List = []IStmt{&ReturnStmt{p.parseExpression(OpAssign)}}
	}
	return
}

func (p *Parser) parseIdentifierExpression(prec OpPrec, ident []byte) IExpr {
	var left IExpr
	left = p.scope.Use(p.ast, ident)
	return p.parseExpressionSuffix(left, prec, OpPrimary)
}

func (p *Parser) parseAsyncExpression(prec OpPrec, async []byte) IExpr {
	// assume we're at a token after async
	var left IExpr
	precLeft := OpPrimary
	if !p.prevLT && p.tt == FunctionToken {
		// primary expression
		funcDecl := p.parseAsyncFuncExpr()
		left = &funcDecl
	} else if !p.prevLT && prec <= OpAssign && (p.tt == OpenParenToken || IsIdentifier(p.tt) || !p.generator && p.tt == YieldToken || p.tt == AwaitToken) {
		// async arrow function expression
		if p.tt == AwaitToken {
			p.fail("arrow function")
			return nil
		}
		arrowFunc := p.parseAsyncArrowFunc()
		left = &arrowFunc
		precLeft = OpAssign
	} else {
		left = p.scope.Use(p.ast, async)
	}
	left = p.parseExpressionSuffix(left, prec, precLeft)
	return left
}

// parseExpression parses an expression that has a precendence of prec or higher.
func (p *Parser) parseExpression(prec OpPrec) IExpr {
	// reparse input if we have / or /= as the beginning of a new expression, this should be a regular expression!
	if p.tt == DivToken || p.tt == DivEqToken {
		p.tt, p.data = p.l.RegExp()
		if p.tt == ErrorToken {
			p.fail("regular expression")
			return nil
		}
	}

	var left IExpr
	precLeft := OpPrimary

	if IsIdentifier(p.tt) && p.tt != AsyncToken {
		left = p.scope.Use(p.ast, p.data)
		p.next()
		return p.parseExpressionSuffix(left, prec, precLeft)
	} else if IsNumeric(p.tt) {
		left = &LiteralExpr{p.tt, p.data}
		p.next()
		return p.parseExpressionSuffix(left, prec, precLeft)
	}

	switch tt := p.tt; tt {
	case StringToken, ThisToken, NullToken, TrueToken, FalseToken, RegExpToken:
		left = &LiteralExpr{p.tt, p.data}
		p.next()
	case OpenBracketToken:
		array := p.parseArrayLiteral()
		left = &array
	case OpenBraceToken:
		object := p.parseObjectLiteral()
		left = &object
	case OpenParenToken:
		// parenthesized expression or arrow parameter list
		if OpAssign < prec {
			// must be a parenthesized expression
			p.next()
			left = &GroupExpr{p.parseExpression(OpExpr)}
			if !p.consume("expression", CloseParenToken) {
				return nil
			}
			break
		}
		return p.parseParenthesizedExpressionOrArrowFunc(prec)
	case NotToken, BitNotToken, TypeofToken, VoidToken, DeleteToken:
		if OpUnary < prec {
			p.fail("expression")
			return nil
		}
		p.next()
		left = &UnaryExpr{tt, p.parseExpression(OpUnary)}
		precLeft = OpUnary
	case AddToken:
		if OpUnary < prec {
			p.fail("expression")
			return nil
		}
		p.next()
		left = &UnaryExpr{PosToken, p.parseExpression(OpUnary)}
		precLeft = OpUnary
	case SubToken:
		if OpUnary < prec {
			p.fail("expression")
			return nil
		}
		p.next()
		left = &UnaryExpr{NegToken, p.parseExpression(OpUnary)}
		precLeft = OpUnary
	case IncrToken:
		if OpUpdate < prec {
			p.fail("expression")
			return nil
		}
		p.next()
		left = &UnaryExpr{PreIncrToken, p.parseExpression(OpUnary)}
		precLeft = OpUnary
	case DecrToken:
		if OpUpdate < prec {
			p.fail("expression")
			return nil
		}
		p.next()
		left = &UnaryExpr{PreDecrToken, p.parseExpression(OpUnary)}
		precLeft = OpUnary
	case AwaitToken:
		// either accepted as IdentifierReference or as AwaitExpression
		if p.async && prec <= OpUnary {
			p.next()
			left = &UnaryExpr{tt, p.parseExpression(OpUnary)}
			precLeft = OpUnary
		} else if p.async {
			p.fail("expression")
			return nil
		} else {
			left = p.scope.Use(p.ast, p.data)
			p.next()
		}
	case NewToken:
		p.next()
		if p.tt == DotToken {
			p.next()
			if !p.consume("new.target expression", TargetToken) {
				return nil
			}
			left = &NewTargetExpr{}
			precLeft = OpMember
		} else {
			newExpr := &NewExpr{p.parseExpression(OpMember), nil}
			if p.tt == OpenParenToken {
				args := p.parseArgs()
				if len(args.List) != 0 || args.Rest != nil {
					newExpr.Args = &args
				}
				precLeft = OpMember
			} else {
				precLeft = OpLHS
			}
			left = newExpr
		}
	case ImportToken:
		// OpMember < prec does never happen
		left = &LiteralExpr{p.tt, p.data}
		p.next()
		if p.tt == DotToken {
			p.next()
			if !p.consume("import.meta expression", MetaToken) {
				return nil
			}
			left = &ImportMetaExpr{}
			precLeft = OpMember
		} else if p.tt != OpenParenToken {
			p.fail("import expression", OpenParenToken)
			return nil
		} else if OpLHS < prec {
			p.fail("expression")
			return nil
		} else {
			precLeft = OpLHS
		}
	case SuperToken:
		// OpMember < prec does never happen
		left = &LiteralExpr{p.tt, p.data}
		p.next()
		if OpLHS < prec && p.tt != DotToken && p.tt != OpenBracketToken {
			p.fail("super expression", OpenBracketToken, DotToken)
			return nil
		} else if p.tt != DotToken && p.tt != OpenBracketToken && p.tt != OpenParenToken {
			p.fail("super expression", OpenBracketToken, OpenParenToken, DotToken)
			return nil
		}
		if OpLHS < prec {
			precLeft = OpMember
		} else {
			precLeft = OpLHS
		}
	case YieldToken:
		// either accepted as IdentifierReference or as YieldExpression
		if p.generator && prec <= OpAssign {
			// YieldExpression
			p.next()
			yieldExpr := YieldExpr{}
			if !p.prevLT {
				yieldExpr.Generator = p.tt == MulToken
				if yieldExpr.Generator {
					p.next()
				}
				yieldExpr.X = p.parseExpression(OpAssign)
			}
			left = &yieldExpr
			precLeft = OpAssign
		} else if p.generator {
			p.fail("expression")
			return nil
		} else {
			left = p.scope.Use(p.ast, p.data)
			p.next()
		}
	case AsyncToken:
		async := p.data
		p.next()
		left = p.parseAsyncExpression(prec, async)
	case ClassToken:
		classDecl := p.parseClassExpr()
		left = &classDecl
	case FunctionToken:
		funcDecl := p.parseFuncExpr()
		left = &funcDecl
	case TemplateToken, TemplateStartToken:
		template := p.parseTemplateLiteral()
		left = &template
	default:
		p.fail("expression")
		return nil
	}
	return p.parseExpressionSuffix(left, prec, precLeft)
}

func (p *Parser) parseExpressionSuffix(left IExpr, prec, precLeft OpPrec) IExpr {
	for {
		switch tt := p.tt; tt {
		case EqToken, MulEqToken, DivEqToken, ModEqToken, ExpEqToken, AddEqToken, SubEqToken, LtLtEqToken, GtGtEqToken, GtGtGtEqToken, BitAndEqToken, BitXorEqToken, BitOrEqToken:
			if OpAssign < prec {
				return left
			} else if precLeft < OpLHS {
				p.fail("expression")
				return nil
			}
			p.next()
			left = &BinaryExpr{tt, left, p.parseExpression(OpAssign)}
			precLeft = OpAssign
		case LtToken, LtEqToken, GtToken, GtEqToken, InToken, InstanceofToken:
			if OpCompare < prec || p.inFor && tt == InToken {
				return left
			} else if precLeft < OpCompare {
				// can only fail after a yield or arrow function expression
				p.fail("expression")
				return nil
			}
			p.next()
			left = &BinaryExpr{tt, left, p.parseExpression(OpShift)}
			precLeft = OpCompare
		case EqEqToken, NotEqToken, EqEqEqToken, NotEqEqToken:
			if OpEquals < prec {
				return left
			} else if precLeft < OpEquals {
				// can only fail after a yield or arrow function expression
				p.fail("expression")
				return nil
			}
			p.next()
			left = &BinaryExpr{tt, left, p.parseExpression(OpCompare)}
			precLeft = OpEquals
		case AndToken:
			if OpAnd < prec {
				return left
			} else if precLeft < OpAnd {
				p.fail("expression")
				return nil
			}
			p.next()
			left = &BinaryExpr{tt, left, p.parseExpression(OpBitOr)}
			precLeft = OpAnd
		case OrToken:
			if OpOr < prec {
				return left
			} else if precLeft < OpOr {
				p.fail("expression")
				return nil
			}
			p.next()
			left = &BinaryExpr{tt, left, p.parseExpression(OpAnd)}
			precLeft = OpOr
		case NullishToken:
			if OpCoalesce < prec {
				return left
			} else if precLeft < OpBitOr && precLeft != OpCoalesce {
				p.fail("expression")
				return nil
			}
			p.next()
			left = &BinaryExpr{tt, left, p.parseExpression(OpBitOr)}
			precLeft = OpCoalesce
		case DotToken:
			// OpMember < prec does never happen
			if precLeft < OpLHS {
				p.fail("expression")
				return nil
			}
			p.next()
			if !IsIdentifierName(p.tt) {
				p.fail("dot expression", IdentifierToken)
				return nil
			}
			left = &DotExpr{left, LiteralExpr{IdentifierToken, p.data}}
			precLeft = OpMember
			p.next()
		case OpenBracketToken:
			// OpMember < prec does never happen
			if precLeft < OpLHS {
				p.fail("expression")
				return nil
			}
			p.next()
			left = &IndexExpr{left, p.parseExpression(OpExpr)}
			if !p.consume("index expression", CloseBracketToken) {
				return nil
			}
			precLeft = OpMember
		case OpenParenToken:
			if OpLHS < prec {
				return left
			} else if precLeft < OpLHS {
				p.fail("expression")
				return nil
			}
			left = &CallExpr{left, p.parseArgs()}
			precLeft = OpLHS
		case TemplateToken, TemplateStartToken:
			// OpMember < prec does never happen
			if precLeft < OpLHS {
				p.fail("expression")
				return nil
			}
			template := p.parseTemplateLiteral()
			template.Tag = left
			left = &template
			precLeft = OpMember
		case OptChainToken:
			if OpLHS < prec {
				return left
			}
			p.next()
			if p.tt == OpenParenToken {
				left = &OptChainExpr{left, &CallExpr{nil, p.parseArgs()}}
			} else if p.tt == OpenBracketToken {
				p.next()
				left = &OptChainExpr{left, &IndexExpr{nil, p.parseExpression(OpExpr)}}
				if !p.consume("optional chaining expression", CloseBracketToken) {
					return nil
				}
			} else if p.tt == TemplateToken || p.tt == TemplateStartToken {
				template := p.parseTemplateLiteral()
				left = &OptChainExpr{left, &template}
			} else if IsIdentifierName(p.tt) {
				left = &OptChainExpr{left, &LiteralExpr{IdentifierToken, p.data}}
				p.next()
			} else {
				p.fail("optional chaining expression", IdentifierToken, OpenParenToken, OpenBracketToken, TemplateToken)
				return nil
			}
			precLeft = OpLHS
		case IncrToken:
			if p.prevLT || OpUpdate < prec {
				return left
			} else if precLeft < OpLHS {
				p.fail("expression")
				return nil
			}
			p.next()
			left = &UnaryExpr{PostIncrToken, left}
			precLeft = OpUpdate
		case DecrToken:
			if p.prevLT || OpUpdate < prec {
				return left
			} else if precLeft < OpLHS {
				p.fail("expression")
				return nil
			}
			p.next()
			left = &UnaryExpr{PostDecrToken, left}
			precLeft = OpUpdate
		case ExpToken:
			if OpExp < prec {
				return left
			} else if precLeft < OpUpdate {
				p.fail("expression")
				return nil
			}
			p.next()
			left = &BinaryExpr{tt, left, p.parseExpression(OpExp)}
			precLeft = OpExp
		case MulToken, DivToken, ModToken:
			if OpMul < prec {
				return left
			} else if precLeft < OpMul {
				p.fail("expression")
				return nil
			}
			p.next()
			left = &BinaryExpr{tt, left, p.parseExpression(OpExp)}
			precLeft = OpMul
		case AddToken, SubToken:
			if OpAdd < prec {
				return left
			} else if precLeft < OpAdd {
				p.fail("expression")
				return nil
			}
			p.next()
			left = &BinaryExpr{tt, left, p.parseExpression(OpMul)}
			precLeft = OpAdd
		case LtLtToken, GtGtToken, GtGtGtToken:
			if OpShift < prec {
				return left
			} else if precLeft < OpShift {
				p.fail("expression")
				return nil
			}
			p.next()
			left = &BinaryExpr{tt, left, p.parseExpression(OpAdd)}
			precLeft = OpShift
		case BitAndToken:
			if OpBitAnd < prec {
				return left
			} else if precLeft < OpBitAnd {
				p.fail("expression")
				return nil
			}
			p.next()
			left = &BinaryExpr{tt, left, p.parseExpression(OpEquals)}
			precLeft = OpBitAnd
		case BitXorToken:
			if OpBitXor < prec {
				return left
			} else if precLeft < OpBitXor {
				p.fail("expression")
				return nil
			}
			p.next()
			left = &BinaryExpr{tt, left, p.parseExpression(OpBitAnd)}
			precLeft = OpBitXor
		case BitOrToken:
			if OpBitOr < prec {
				return left
			} else if precLeft < OpBitOr {
				p.fail("expression")
				return nil
			}
			p.next()
			left = &BinaryExpr{tt, left, p.parseExpression(OpBitXor)}
			precLeft = OpBitOr
		case QuestionToken:
			if OpAssign < prec {
				return left
			} else if precLeft < OpCoalesce {
				p.fail("expression")
				return nil
			}
			p.next()
			ifExpr := p.parseExpression(OpAssign)
			if !p.consume("conditional expression", ColonToken) {
				return nil
			}
			elseExpr := p.parseExpression(OpAssign)
			left = &CondExpr{left, ifExpr, elseExpr}
			precLeft = OpAssign
		case CommaToken:
			if OpExpr < prec {
				return left
			}
			p.next()
			left = &BinaryExpr{tt, left, p.parseExpression(OpAssign)}
			precLeft = OpExpr
		case ArrowToken:
			// handle identifier => ..., where identifier could also be yield or await
			if OpAssign < prec {
				return left
			} else if precLeft < OpPrimary {
				p.fail("expression")
				return nil
			}

			ref, ok := left.(VarRef)
			if !ok {
				p.fail("expression")
				return nil
			}

			arrowFunc := p.parseIdentifierArrowFunc(ref)
			left = &arrowFunc
			precLeft = OpAssign
		default:
			return left
		}
	}
}

func (p *Parser) parseAssignmentExpression() IExpr {
	// this could be a BindingElement or an AssignmentExpression. Here we handle BindingIdentifier with a possible Initializer, BindingPattern will be handled by parseArrayLiteral or parseObjectLiteral
	if p.assumeArrowFunc && IsIdentifier(p.tt) {
		tt := p.tt
		data := p.data
		p.next()
		if p.tt == EqToken || p.tt == CommaToken || p.tt == CloseParenToken || p.tt == CloseBraceToken || p.tt == CloseBracketToken {
			var left IExpr
			left, _ = p.scope.Declare(p.ast, ArgumentDecl, data) // cannot fail
			return p.parseExpressionSuffix(left, OpAssign, OpPrimary)
		}
		p.assumeArrowFunc = false
		if tt == AsyncToken {
			return p.parseAsyncExpression(OpAssign, data)
		}
		return p.parseIdentifierExpression(OpAssign, data)
	} else if p.tt != OpenBracketToken && p.tt != OpenBraceToken {
		p.assumeArrowFunc = false
	}
	return p.parseExpression(OpAssign)
}

func (p *Parser) parseParenthesizedExpressionOrArrowFunc(prec OpPrec) IExpr {
	var left IExpr
	precLeft := OpPrimary

	// expect to be at (
	p.next()

	arrowFunc := ArrowFunc{}
	parent := p.enterScope(&arrowFunc.Scope, true)
	parentAssumeArrowFunc := p.assumeArrowFunc
	p.assumeArrowFunc = true

	// parse a parenthesized expression but assume we might be parsing an arrow function. If this is really an arrow function, parsing as a parenthesized expression cannot fail as AssignmentExpression, ArrayLiteral, and ObjectLiteral are supersets of SingleNameBinding, ArrayBindingPattern, and ObjectBindingPattern respectively. Any identifier that would be a BindingIdentifier in case of an arrow function, will be added as such. If finally this is not an arrow function, we will demote those variables an undeclared and merge them with the parent scope.

	var list []IExpr
	var rest IBinding
	for p.tt != CloseParenToken && p.tt != ErrorToken {
		if p.tt == EllipsisToken && p.assumeArrowFunc {
			p.next()
			rest = p.parseBinding(ArgumentDecl)
			break
		}

		list = append(list, p.parseAssignmentExpression())
		if p.tt != CommaToken {
			break
		}
		p.next()
	}
	if p.tt != CloseParenToken {
		p.fail("expression")
		return nil
	}
	p.next()

	if p.tt == ArrowToken && p.assumeArrowFunc {
		parentAsync, parentGenerator := p.async, p.generator
		p.async, p.generator = false, false

		// arrow function
		arrowFunc.Params = Params{List: make([]BindingElement, len(list)), Rest: rest}
		for i, item := range list {
			arrowFunc.Params.List[i] = p.exprToBindingElement(item) // can not fail when assumArrowFunc is set
		}
		arrowFunc.Body = p.parseArrowFuncBody()

		p.async, p.generator, p.assumeArrowFunc = parentAsync, parentGenerator, parentAssumeArrowFunc
		p.exitScope(parent)

		left = &arrowFunc
		precLeft = OpAssign
	} else if len(list) == 0 || rest != nil {
		p.fail("arrow function", ArrowToken)
		return nil
	} else {
		p.assumeArrowFunc = parentAssumeArrowFunc
		p.exitScope(parent)

		arrowFunc.Scope.UndeclareScope(p.ast)
		// TODO: Parent and Func pointers are bad for any nested FuncDecl/ArrowFunc inside, maybe not a problem?

		// parenthesized expression
		left = list[0]
		for _, item := range list[1:] {
			left = &BinaryExpr{CommaToken, left, item}
		}
		left = &GroupExpr{left}
	}
	return p.parseExpressionSuffix(left, prec, precLeft)
}

// exprToBinding converts a CoverParenthesizedExpressionAndArrowParameterList into FormalParameters
// Any unbound variables of the parameters (Initializer, ComputedPropertyName) are kept in the parent scope
func (p *Parser) exprToBinding(expr IExpr) (binding IBinding) {
	if ref, ok := expr.(VarRef); ok {
		binding = ref
	} else if array, ok := expr.(*ArrayExpr); ok {
		bindingArray := BindingArray{}
		for _, item := range array.List {
			if item.Spread {
				// can only BindingIdentifier or BindingPattern
				bindingArray.Rest = p.exprToBinding(item.Value)
				break
			}
			var bindingElement BindingElement
			bindingElement = p.exprToBindingElement(item.Value)
			bindingArray.List = append(bindingArray.List, bindingElement)
		}
		binding = &bindingArray
	} else if object, ok := expr.(*ObjectExpr); ok {
		bindingObject := BindingObject{}
		for _, item := range object.List {
			if item.Spread {
				// can only be BindingIdentifier
				bindingObject.Rest = item.Value.(VarRef)
				break
			}
			var bindingElement BindingElement
			bindingElement.Binding = p.exprToBinding(item.Value)
			if item.Init != nil {
				bindingElement.Default = item.Init
			}
			bindingObject.List = append(bindingObject.List, BindingObjectItem{Key: item.Name, Value: bindingElement})
		}
		binding = &bindingObject
	}
	return
}

func (p *Parser) exprToBindingElement(expr IExpr) (bindingElement BindingElement) {
	if assign, ok := expr.(*BinaryExpr); ok && assign.Op == EqToken {
		bindingElement.Binding = p.exprToBinding(assign.X)
		bindingElement.Default = assign.Y
	} else {
		bindingElement.Binding = p.exprToBinding(expr)
	}
	return
}

func (p *Parser) isIdentifierReference(tt TokenType) bool {
	return IsIdentifier(tt) || tt == YieldToken && !p.generator || tt == AwaitToken && !p.async
}
