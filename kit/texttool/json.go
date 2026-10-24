// SPDX-License-Identifier: Apache-2.0
package texttool

import (
 "bytes"
 "encoding/json"
 "errors"
 "io"
 "unicode/utf8"
)

var errSyntax=errors.New("invalid text tool call")

func jsonValue(d *json.Decoder, depth int) (any,error) {
 if depth>64 {return nil,errSyntax}
 token,err:=d.Token();if err!=nil {return nil,err}
 delim,ok:=token.(json.Delim);if !ok {return token,nil}
 switch delim {
 case '{':
  out:=map[string]any{}
  for d.More() {
   token,err:=d.Token();if err!=nil {return nil,err}
   key,ok:=token.(string);if !ok {return nil,errSyntax}
   if _,exists:=out[key];exists {return nil,errSyntax}
   value,err:=jsonValue(d,depth+1);if err!=nil {return nil,err};out[key]=value
  }
  end,err:=d.Token();if err!=nil || end!=json.Delim('}') {return nil,errSyntax}
  return out,nil
 case '[':
  out:=[]any{}
  for d.More() {value,err:=jsonValue(d,depth+1);if err!=nil {return nil,err};out=append(out,value)}
  end,err:=d.Token();if err!=nil || end!=json.Delim(']') {return nil,errSyntax}
  return out,nil
 }
 return nil,errSyntax
}

func decodeJSON(raw []byte) (any,error) {
 if !utf8.Valid(raw) {return nil,errSyntax}
 d:=json.NewDecoder(bytes.NewReader(raw));d.UseNumber()
 value,err:=jsonValue(d,0);if err!=nil {return nil,err}
 if _,err=d.Token();err!=io.EOF {return nil,errSyntax}
 return value,nil
}

func objectJSON(raw []byte) (json.RawMessage,error) {
 value,err:=decodeJSON(raw);if err!=nil {return nil,err}
 if _,ok:=value.(map[string]any);!ok {return nil,errSyntax}
 return json.Marshal(value)
}

// Read one JSON value without accepting duplicate keys or rounding numbers.
func jsonPrefix(raw string) (json.RawMessage,string,error) {
 d:=json.NewDecoder(bytes.NewBufferString(raw));d.UseNumber()
 value,err:=jsonValue(d,0);if err!=nil {return nil,"",err}
 encoded,err:=json.Marshal(value)
 return encoded,raw[d.InputOffset():],err
}
