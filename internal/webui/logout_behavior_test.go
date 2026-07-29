package webui

import (
	"os/exec"
	"testing"
)

func TestConsoleLogoutFailureBlocksBearerUntilRetry(t *testing.T) {
	script, _ := elementBody(string(document), "script")
	logout, _ := functionBody(script, "logout")
	logout = "async " + logout
	bearer, _ := functionBody(script, "bearer")
	bearer = "async " + bearer
	enter, _ := functionBody(script, "enterBearer")
	harness := `let statuses=[503,204],entered=[],reported=0;const token={value:"compat",setCustomValidity(){},reportValidity(){reported++}},$=()=>token,api=async()=>({status:statuses.shift()});function enter(t){entered.push(t)}` + logout + enter + bearer + `;await bearer();if(entered.length||reported!==1)throw Error("failure entered bearer");await bearer();if(entered[0]!=="compat")throw Error("retry did not enter bearer");`
	if output, err := exec.Command(requireNode(t), "--input-type=module", "-e", harness).CombinedOutput(); err != nil {
		t.Fatalf("console logout behavior: %v\n%s", err, output)
	}
}

func TestAdminLogoutFailureBlocksBearerUntilRetry(t *testing.T) {
	script, _ := elementBody(string(adminDocument), "script")
	logout, _ := functionBody(script, "logout")
	logout = "async " + logout
	enter, _ := functionBody(script, "enterBearer")
	enter = "async " + enter
	harness := `let statuses=[503,204],mode="session",token="",shown="",loads=0;const $=()=>({value:"compat"}),api=async()=>({status:statuses.shift()}),sessionStorage={setItem(){}},showAccess=m=>shown=m,load=()=>{loads++};` + logout + enter + `;await enterBearer();if(mode==="bearer"||!shown||loads)throw Error("failure entered bearer");shown="";await enterBearer();if(mode!=="bearer"||token!=="compat"||loads!==1)throw Error("retry did not enter bearer");`
	if output, err := exec.Command(requireNode(t), "--input-type=module", "-e", harness).CombinedOutput(); err != nil {
		t.Fatalf("admin logout behavior: %v\n%s", err, output)
	}
}
