// SPDX-License-Identifier: Apache-2.0
package texttool

import (
 "strings"
 "unicode/utf8"
)

// Detect examines a completed assistant turn. Only a standalone call at the
// end of visible text qualifies; quoted examples and reasoning are not calls.
// Multiple calls are rejected together, never partially dispatched.
func Detect(turn Turn, declarations []Declaration) Decision {
 none := Decision{Action: None, VisibleText: turn.Text}
 if len(turn.ToolCalls)>0 { return none }
 switch turn.StopReason {case "stop", "end_turn", "length", "max_tokens", "completed": default: return none}
 start := candidateStart(turn.Text)
 if start<0 {return none}
 raw:=strings.TrimSpace(turn.Text[start:])
 restart:=Decision{Detected:true, Action:RestartFreshSession, ExtraTurns:1, FreshSessionStarts:1}
 if len(raw)>1<<20 || !utf8.ValidString(raw) {return restart}
 name, args, params, rest, err:=parse(raw)
 if err!=nil {return restart}
 if strings.TrimSpace(rest)!="" {
  if candidateStart(rest)>=0 {return restart}
  return none // The call is quoted or discussed before the final prose.
 }
 var decl *Declaration
 for i:=range declarations {if declarations[i].Name==name {decl=&declarations[i];break}}
 if decl==nil && strings.HasPrefix(name,"_") {
  for i:=range declarations {if declarations[i].Name==name[1:] {decl=&declarations[i];break}}
 }
 if decl==nil {return restart}
 if params!=nil {
  args,err=parameterArguments(params,decl.Schema)
  if err!=nil {return restart}
 }
 canonical,err:=objectJSON(args)
 if err!=nil {return restart}
 return Decision{Detected:true, Action:ParseAndDispatch, ParsedCall:&Call{Name:decl.Name, Arguments:canonical}}
}

// Scan only Markdown-visible line starts. The payload is subsequently parsed
// from the original text, so code-like strings inside arguments are preserved.
func candidateStart(text string) int {
 offset:=0
 fence:=byte(0)
 width:=0
 thinking:=""
 inDiff:=false
 for _,line:=range strings.SplitAfter(text,"\n") {
  trimmed:=strings.TrimSpace(line)
  indent:=len(line)-len(strings.TrimLeft(line," "))
  if thinking!="" {
   if strings.Contains(line,"</"+thinking+">") {thinking=""}
   offset+=len(line);continue
  }
  if fence!=0 {
   if indent<=3 && len(trimmed)>=width && strings.Trim(trimmed,string(fence))=="" {fence=0}
   offset+=len(line);continue
  }
  if indent>=4 || strings.HasPrefix(line,"\t") {offset+=len(line);continue}
  if len(trimmed)>=3 && (strings.HasPrefix(trimmed,"```") || strings.HasPrefix(trimmed,"~~~")) {
   fence=trimmed[0];width=len(trimmed)-len(strings.TrimLeft(trimmed,string(fence)))
   offset+=len(line);continue
  }
  if strings.HasPrefix(trimmed,"diff --git ") || strings.HasPrefix(trimmed,"@@ ") {inDiff=true}
  if inDiff {
   if trimmed!="" && !strings.HasPrefix(line," ") && !strings.ContainsAny(trimmed[:1],"+-@\\") && !strings.HasPrefix(trimmed,"diff ") && !strings.HasPrefix(trimmed,"index ") {inDiff=false}
   if inDiff {offset+=len(line);continue}
  }
  for _,tag:=range []string{"think","thinking","analysis"} {
   if strings.Contains(trimmed,"<"+tag+">") && !strings.Contains(trimmed,"</"+tag+">") {thinking=tag}
  }
  if thinking!="" || strings.HasPrefix(trimmed,"<think>") || strings.HasPrefix(trimmed,"<thinking>") || strings.HasPrefix(trimmed,"<analysis>") {offset+=len(line);continue}
  if marker(trimmed) {return offset+indent}
  offset+=len(line)
 }
 return -1
}

func marker(s string) bool {
 if strings.HasPrefix(s,"[tool_use") {
  tail:=s[len("[tool_use"):]
  return tail=="" || strings.HasPrefix(tail," name=") || strings.HasPrefix(tail," id=") || tail=="]"
 }
 for _,tag:=range []string{"tool_use","function_calls","invoke","antml:function_calls","antml:invoke","tool_call"} {
  prefix:="<"+tag
  if !strings.HasPrefix(s,prefix) {continue}
  tail:=s[len(prefix):]
  if tail=="" || strings.HasPrefix(tail," name=") || strings.HasPrefix(tail," id=") {return true}
  if strings.HasPrefix(tail,">") {
   body:=strings.TrimSpace(tail[1:])
   return body=="" || strings.HasPrefix(body,"{") || strings.HasPrefix(body,"<")
  }
 }
 return false
}
