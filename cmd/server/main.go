package main

import (
 "crypto/rand"
 "database/sql"
 "encoding/base64"
 "encoding/json"
 "log"
 "net/http"
 "os"
 "strings"
 "sync"
 "time"
 "github.com/gorilla/websocket"
 _ "modernc.org/sqlite"
)

type Metric struct { At time.Time `json:"at"`; CPU,Memory,Disk,Load1 float64; RxBPS,TxBPS,RxTotal,TxTotal uint64; IPv4 string }
type VPS struct { ID string `json:"id"`; Name string `json:"name"`; Token string `json:"token,omitempty"`; Online bool `json:"online"`; Metric; DayBytes,MonthBytes,TotalBytes,DayLimit,MonthLimit uint64; BillingDay int `json:"billingDay"`; PingTarget string `json:"pingTarget"`; ChangeIPCommand string `json:"changeIpCommand,omitempty"`; CFZoneID string `json:"cfZoneId,omitempty"`; CFRecordID string `json:"cfRecordId,omitempty"`; CFRecordName string `json:"cfRecordName,omitempty"`; CFToken string `json:"cfToken,omitempty"` }
type App struct{ db *sql.DB; mu sync.RWMutex; live map[string]Metric; agents map[string]*websocket.Conn; views map[*websocket.Conn]bool; up websocket.Upgrader; password string }

func main(){db,e:=sql.Open("sqlite",env("DATABASE_PATH","panel.db"));if e!=nil{log.Fatal(e)};db.SetMaxOpenConns(1);_,e=db.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE IF NOT EXISTS vps(id TEXT PRIMARY KEY,name TEXT,token TEXT UNIQUE,billing_day INTEGER DEFAULT 1,day_limit INTEGER DEFAULT 0,month_limit INTEGER DEFAULT 0,ping_target TEXT DEFAULT '',change_cmd TEXT DEFAULT '',cf_zone TEXT DEFAULT '',cf_record TEXT DEFAULT '',cf_name TEXT DEFAULT '',cf_token TEXT DEFAULT '',ipv4 TEXT DEFAULT '',last_rx INTEGER DEFAULT 0,last_tx INTEGER DEFAULT 0,day_key TEXT DEFAULT '',month_key TEXT DEFAULT '',day_bytes INTEGER DEFAULT 0,month_bytes INTEGER DEFAULT 0,total_bytes INTEGER DEFAULT 0); CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY AUTOINCREMENT,vps_id TEXT,at DATETIME,type TEXT,detail TEXT);`);if e!=nil{log.Fatal(e)};a:=&App{db:db,live:map[string]Metric{},agents:map[string]*websocket.Conn{},views:map[*websocket.Conn]bool{},password:env("ADMIN_PASSWORD","admin"),up:websocket.Upgrader{CheckOrigin:func(*http.Request)bool{return true}}};m:=http.NewServeMux();m.HandleFunc("/api/login",a.login);m.HandleFunc("/api/vps",a.auth(a.vps));m.HandleFunc("/api/vps/",a.auth(a.one));m.HandleFunc("/ws/live",a.auth(a.view));m.HandleFunc("/agent/ws",a.agent);m.HandleFunc("/install-agent.sh",func(w http.ResponseWriter,r *http.Request){http.ServeFile(w,r,"install-agent.sh")});m.Handle("/",http.FileServer(http.Dir("web")));log.Println("VPS Pulse listening :8080");log.Fatal(http.ListenAndServe(":8080",m))}
func(a *App)login(w http.ResponseWriter,r *http.Request){var x struct{Password string `json:"password"`};json.NewDecoder(r.Body).Decode(&x);if x.Password!=a.password{http.Error(w,"密码错误",401);return};http.SetCookie(w,&http.Cookie{Name:"session",Value:a.password,Path:"/",HttpOnly:true,SameSite:http.SameSiteStrictMode,MaxAge:604800});write(w,map[string]bool{"ok":true})}
func(a *App)auth(n http.HandlerFunc)http.HandlerFunc{return func(w http.ResponseWriter,r *http.Request){c,e:=r.Cookie("session");if e!=nil||c.Value!=a.password{http.Error(w,"unauthorized",401);return};n(w,r)}}
func(a *App)vps(w http.ResponseWriter,r *http.Request){if r.Method=="POST"{var v VPS;json.NewDecoder(r.Body).Decode(&v);v.ID=tok(6);v.Token=tok(24);if v.BillingDay==0{v.BillingDay=1};_,e:=a.db.Exec(`INSERT INTO vps(id,name,token,billing_day,ping_target) VALUES(?,?,?,?,?)`,v.ID,v.Name,v.Token,v.BillingDay,v.PingTarget);if e!=nil{http.Error(w,e.Error(),500);return};write(w,map[string]any{"vps":v,"install":"curl -fsSL "+env("PUBLIC_URL","http://localhost:8080")+"/install-agent.sh | sudo bash -s -- '"+env("PUBLIC_URL","http://localhost:8080")+"' '"+v.Token+"'"});return};rows,_:=a.db.Query(`SELECT id,name,token,billing_day,day_limit,month_limit,ping_target,change_cmd,cf_zone,cf_record,cf_name,cf_token,ipv4,day_bytes,month_bytes,total_bytes FROM vps`);defer rows.Close();out:=[]VPS{};for rows.Next(){var v VPS;rows.Scan(&v.ID,&v.Name,&v.Token,&v.BillingDay,&v.DayLimit,&v.MonthLimit,&v.PingTarget,&v.ChangeIPCommand,&v.CFZoneID,&v.CFRecordID,&v.CFRecordName,&v.CFToken,&v.IPv4,&v.DayBytes,&v.MonthBytes,&v.TotalBytes);a.mu.RLock();m,ok:=a.live[v.ID];a.mu.RUnlock();if ok{v.Metric=m;v.Online=time.Since(m.At)<15*time.Second};v.Token="";v.CFToken="";out=append(out,v)};write(w,out)}
func(a *App)one(w http.ResponseWriter,r *http.Request){
	p:=strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path,"/api/vps/"),"/"),"/")
	id:=p[0]
	if id==""{http.Error(w,"缺少 VPS ID",400);return}
	if r.Method=="DELETE"{
		tx,e:=a.db.Begin();if e!=nil{http.Error(w,e.Error(),500);return}
		defer tx.Rollback()
		if _,e=tx.Exec(`DELETE FROM events WHERE vps_id=?`,id);e!=nil{http.Error(w,e.Error(),500);return}
		res,e:=tx.Exec(`DELETE FROM vps WHERE id=?`,id);if e!=nil{http.Error(w,e.Error(),500);return}
		n,_:=res.RowsAffected();if n==0{http.Error(w,"VPS 不存在",404);return}
		if e=tx.Commit();e!=nil{http.Error(w,e.Error(),500);return}
		a.mu.Lock();if c:=a.agents[id];c!=nil{_ = c.Close()};delete(a.agents,id);delete(a.live,id);a.mu.Unlock()
		w.WriteHeader(http.StatusNoContent);return
	}
	if len(p)>1{
		a.mu.RLock();c:=a.agents[id];a.mu.RUnlock()
		if c==nil{http.Error(w,"Agent 离线",409);return}
		c.WriteJSON(map[string]any{"type":"action","action":p[1]});write(w,map[string]bool{"ok":true});return
	}
	if r.Method!="PUT"{http.Error(w,"method",405);return}
	var v VPS;json.NewDecoder(r.Body).Decode(&v)
	_,e:=a.db.Exec(`UPDATE vps SET name=?,billing_day=?,day_limit=?,month_limit=?,ping_target=?,change_cmd=?,cf_zone=?,cf_record=?,cf_name=?,cf_token=CASE WHEN ?='' THEN cf_token ELSE ? END WHERE id=?`,v.Name,v.BillingDay,v.DayLimit,v.MonthLimit,v.PingTarget,v.ChangeIPCommand,v.CFZoneID,v.CFRecordID,v.CFRecordName,v.CFToken,v.CFToken,id)
	if e!=nil{http.Error(w,e.Error(),500);return}
	a.mu.RLock();c:=a.agents[id];a.mu.RUnlock();if c!=nil{c.WriteJSON(map[string]any{"type":"config","pingTarget":v.PingTarget,"changeIpCommand":v.ChangeIPCommand})};write(w,map[string]bool{"ok":true})
}
func(a *App)agent(w http.ResponseWriter,r *http.Request){var id string;var bill int;var ping,cmd string;e:=a.db.QueryRow(`SELECT id,billing_day,ping_target,change_cmd FROM vps WHERE token=?`,r.URL.Query().Get("token")).Scan(&id,&bill,&ping,&cmd);if e!=nil{http.Error(w,"token",401);return};c,e:=a.up.Upgrade(w,r,nil);if e!=nil{return};a.mu.Lock();a.agents[id]=c;a.mu.Unlock();c.WriteJSON(map[string]any{"type":"config","pingTarget":ping,"changeIpCommand":cmd});defer func(){a.mu.Lock();delete(a.agents,id);a.mu.Unlock();c.Close()}();for{var x struct{Type string `json:"type"`; Metric *Metric `json:"metric"`; Event,Detail,IPv4 string};if c.ReadJSON(&x)!=nil{return};if x.Metric!=nil{x.Metric.At=time.Now();a.record(id,bill,*x.Metric);a.mu.Lock();a.live[id]=*x.Metric;for v:=range a.views{v.WriteJSON(map[string]any{"type":"metric","vpsId":id,"metric":x.Metric})};a.mu.Unlock()}else{a.db.Exec(`INSERT INTO events(vps_id,at,type,detail) VALUES(?,?,?,?)`,id,time.Now(),x.Event,x.Detail)}}}
func(a *App)record(id string,bill int,m Metric){var lr,lt,db,mb,t uint64;var dk,mk string;a.db.QueryRow(`SELECT last_rx,last_tx,day_bytes,month_bytes,total_bytes,day_key,month_key FROM vps WHERE id=?`,id).Scan(&lr,&lt,&db,&mb,&t,&dk,&mk);delta:=uint64(0);if m.RxTotal>=lr{delta+=m.RxTotal-lr};if m.TxTotal>=lt{delta+=m.TxTotal-lt};now:=time.Now().In(time.FixedZone("CST",28800));nd:=now.Format("2006-01-02");anchor:=time.Date(now.Year(),now.Month(),bill,0,0,0,0,now.Location());if now.Before(anchor){anchor=anchor.AddDate(0,-1,0)};nm:=anchor.Format("2006-01-02");if dk!=nd{db=0};if mk!=nm{mb=0};a.db.Exec(`UPDATE vps SET ipv4=?,last_rx=?,last_tx=?,day_key=?,month_key=?,day_bytes=?,month_bytes=?,total_bytes=? WHERE id=?`,m.IPv4,m.RxTotal,m.TxTotal,nd,nm,db+delta,mb+delta,t+delta,id)}
func(a *App)view(w http.ResponseWriter,r *http.Request){c,e:=a.up.Upgrade(w,r,nil);if e!=nil{return};a.mu.Lock();a.views[c]=true;a.mu.Unlock();defer func(){a.mu.Lock();delete(a.views,c);a.mu.Unlock()}();for{if _,_,e:=c.ReadMessage();e!=nil{return}}}
func write(w http.ResponseWriter,v any){w.Header().Set("Content-Type","application/json");json.NewEncoder(w).Encode(v)}
func tok(n int)string{b:=make([]byte,n);rand.Read(b);return base64.RawURLEncoding.EncodeToString(b)}
func env(k,d string)string{if v:=os.Getenv(k);v!=""{return v};return d}
