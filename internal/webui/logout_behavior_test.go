package webui

import (
	"os/exec"
	"testing"
)

func TestConsoleLogoutFailureBlocksBearerUntilRetry(t *testing.T) {
	script, _ := elementBody(string(document), "script")
	api, _ := functionBody(script, "api")
	logout, _ := functionBody(script, "logout")
	logout = "async " + logout
	bearer, _ := functionBody(script, "bearer")
	bearer = "async " + bearer
	enter, _ := functionBody(script, "enterBearer")
	harness := `let statuses=[503,204],entered=[],reported=0,validity=[],calls=[];const state={token:""},token={value:"compat",setCustomValidity(v){validity.push(v)},reportValidity(){reported++}},$=()=>token,fetch=async(path,options)=>{calls.push([path,options]);return {status:statuses.shift()}};function enter(t){entered.push(t)}` + api + logout + enter + bearer + `;await bearer();if(entered.length||reported!==1)throw Error("failure entered bearer");await bearer();if(entered[0]!=="compat")throw Error("retry did not enter bearer");if(validity.join("|")!=="|Failed.|")throw Error("retry did not clear validity");if(calls.some(([,o])=>o.credentials!=="same-origin"))throw Error("missing same-origin credentials");`
	if output, err := exec.Command(requireNode(t), "--input-type=module", "-e", harness).CombinedOutput(); err != nil {
		t.Fatalf("console logout behavior: %v\n%s", err, output)
	}
}

func TestAdminStoredTokenStartupRetriesOneCredentiallessLogout(t *testing.T) {
	script, _ := elementBody(string(adminDocument), "script")
	api, _ := functionBody(script, "api")
	logout, _ := functionBody(script, "logout")
	logout = "async " + logout
	enter, _ := functionBody(script, "enterBearer")
	enter = "async " + enter
	start, _ := functionBody(script, "start")
	start = "async " + start
	harness := `let mode="anonymous",token="",pending="stored",shown="",loads=0,calls=[],responses=[{ok:true,json:async()=>({token_login:true,providers:[]})},{ok:false,status:401},{status:503},{status:204}];const elements={token:{value:"",focus(){}},["token-form"]:{hidden:false}},$=id=>elements[id],fetch=async(path,options={})=>{calls.push([path,options]);return responses.shift()},sessionStorage={setItem(){}},renderProviders=()=>{},showAccess=m=>shown=m||"",load=async()=>{loads++};` + api + logout + enter + start + `;await start();if(mode==="bearer"||!shown||loads||pending!=="stored")throw Error("failed startup lost retry");await enterBearer();if(mode!=="bearer"||token!=="stored"||pending||loads!==1)throw Error("retry did not enter bearer");const logoutCalls=calls.filter(([p])=>p==="/auth/logout");if(logoutCalls.length!==2)throw Error("successful retry required second logout");for(const [path,options] of calls){if(options.credentials!=="same-origin")throw Error("missing same-origin credentials: "+path);if((path==="/v1/auth/session"||path==="/auth/logout")&&new Headers(options.headers||{}).has("Authorization"))throw Error("credential leaked: "+path)}responses.push({status:204});await logout();if(new Headers(calls.at(-1)[1].headers||{}).has("Authorization"))throw Error("logout leaked bearer");responses.push({ok:true});await api("/v1/admin/overview");if(!new Headers(calls.at(-1)[1].headers).has("Authorization"))throw Error("bearer mode not enabled");responses.push({ok:false},{ok:true});await start();const sessionCall=calls.filter(([p])=>p==="/v1/auth/session").at(-1);if(new Headers(sessionCall[1].headers||{}).has("Authorization"))throw Error("session discovery leaked bearer");`
	if output, err := exec.Command(requireNode(t), "--input-type=module", "-e", harness).CombinedOutput(); err != nil {
		t.Fatalf("admin logout behavior: %v\n%s", err, output)
	}
}
