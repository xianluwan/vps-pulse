package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const cloudflareAPI = "https://api.cloudflare.com/client/v4"

type cfEnvelope struct {
	Success bool `json:"success"`
	Errors []struct{ Message string `json:"message"` } `json:"errors"`
	Result json.RawMessage `json:"result"`
}

type cfZone struct { ID string `json:"id"`; Name string `json:"name"` }
type cfRecord struct { ID string `json:"id"`; Name string `json:"name"`; Content string `json:"content"` }

func (a *App) updateCloudflareDNS(id string) error {
	var domain, token, ip string
	if err:=a.db.QueryRow(`SELECT cf_name,cf_token,ipv4 FROM vps WHERE id=?`,id).Scan(&domain,&token,&ip);err!=nil{return err}
	domain=strings.TrimSpace(strings.TrimSuffix(domain,"."))
	if domain==""||token==""{return fmt.Errorf("请先填写完整域名和 Cloudflare API Token")}
	if ip==""{return fmt.Errorf("Agent 尚未上报公网 IPv4")}
	zone,err:=findCFZone(domain,token);if err!=nil{return err}
	record,found,err:=findCFRecord(zone.ID,domain,token);if err!=nil{return err}
	payload:=map[string]any{"type":"A","name":domain,"content":ip,"ttl":60,"proxied":false}
	if found&&record.Content==ip{return nil}
	method,path:=http.MethodPost,"/zones/"+zone.ID+"/dns_records"
	if found{method=http.MethodPut;path+="/"+record.ID}
	if _,err=cfRequest(method,path,token,payload);err!=nil{return fmt.Errorf("更新 %s 失败: %w",domain,err)}
	return nil
}

func findCFZone(domain,token string)(cfZone,error){
	parts:=strings.Split(domain,".")
	for i:=0;i<len(parts)-1;i++{
		name:=strings.Join(parts[i:],".")
		raw,err:=cfRequest(http.MethodGet,"/zones?name="+url.QueryEscape(name),token,nil);if err!=nil{return cfZone{},err}
		var zones []cfZone;if json.Unmarshal(raw,&zones)==nil&&len(zones)>0{return zones[0],nil}
	}
	return cfZone{},fmt.Errorf("未找到域名 %s 对应的 Cloudflare Zone，请检查 Token 权限",domain)
}

func findCFRecord(zoneID,domain,token string)(cfRecord,bool,error){
	path:="/zones/"+zoneID+"/dns_records?type=A&name="+url.QueryEscape(domain)
	raw,err:=cfRequest(http.MethodGet,path,token,nil);if err!=nil{return cfRecord{},false,err}
	var records []cfRecord;if err=json.Unmarshal(raw,&records);err!=nil{return cfRecord{},false,err}
	if len(records)==0{return cfRecord{},false,nil};return records[0],true,nil
}

func cfRequest(method,path,token string,payload any)(json.RawMessage,error){
	var body io.Reader
	if payload!=nil{b,_:=json.Marshal(payload);body=bytes.NewReader(b)}
	req,err:=http.NewRequest(method,cloudflareAPI+path,body);if err!=nil{return nil,err}
	req.Header.Set("Authorization","Bearer "+token);req.Header.Set("Content-Type","application/json")
	client:=http.Client{Timeout:15*time.Second};resp,err:=client.Do(req);if err!=nil{return nil,err};defer resp.Body.Close()
	b,err:=io.ReadAll(io.LimitReader(resp.Body,1<<20));if err!=nil{return nil,err}
	var env cfEnvelope;if err=json.Unmarshal(b,&env);err!=nil{return nil,fmt.Errorf("Cloudflare 返回无效响应: HTTP %d",resp.StatusCode)}
	if !env.Success{msg:=resp.Status;if len(env.Errors)>0{msg=env.Errors[0].Message};return nil,fmt.Errorf("%s",msg)}
	return env.Result,nil
}
