# Annotation 速查（google.api.http + ai/v1）

載入時機：新增或修改 RPC、需要 HTTP/JSON 存取、或要把 RPC 暴露給 AI agent 時。

## google.api.http（REST/JSON 映射）

需在 proto `import "google/api/annotations.proto";`（buf dep `buf.build/googleapis/googleapis`）。

```protobuf
service ResourceService {
  // GET + path param
  rpc GetResource(GetResourceRequest) returns (Resource) {
    option (google.api.http) = { get: "/v1/resources/{id}" };
  }
  // GET + query param（未在 path 的 scalar 自動成為 query string）
  rpc ListResources(ListResourcesRequest) returns (ListResourcesResponse) {
    option (google.api.http) = { get: "/v1/resources" };
  }
  // POST + 整個 body
  rpc CreateResource(CreateResourceRequest) returns (Resource) {
    option (google.api.http) = { post: "/v1/resources" body: "*" };
  }
  // PATCH + flat body（部分更新：request 用 optional 欄位，未設欄位跳過驗證
  // 且不覆寫——見 proto/resource/v1 的 UpdateResourceRequest）
  rpc UpdateResourcePartial(UpdateResourceRequest) returns (Resource) {
    option (google.api.http) = { patch: "/v1/resources/{id}" body: "*" };
  }
  // PUT
  rpc ReplaceResource(ReplaceResourceRequest) returns (Resource) {
    option (google.api.http) = { put: "/v1/resources/{id}" body: "*" };
  }
  // DELETE
  rpc DeleteResource(DeleteResourceRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = { delete: "/v1/resources/{id}" };
  }
}
```

規則：
- path 內 `{id}` 對應 request 同名欄位；`{resource.id}` 對應巢狀欄位。
- `body: "*"` 整個 request 當 body；`body: "resource"` 只取該欄位；不寫 body 則欄位走 query（適合 GET）。
- 未出現在 path 的 scalar 欄位在 GET 自動成為 query parameter。

## ai/v1 annotation（AI-skill 暴露）

```protobuf
import "gortexa/ai/v1/annotations.proto";

service ResourceService {
  rpc GetResource(GetResourceRequest) returns (Resource) {
    option (gortexa.ai.v1.ai_tool) = {
      expose: true
      name: "get_resource"
      description: "Fetch a single resource by id. Use when you have a resource id and need its current state."
      read_only: true
    };
  }
  rpc DeleteResource(DeleteResourceRequest) returns (google.protobuf.Empty) {
    option (gortexa.ai.v1.ai_tool) = {
      expose: true
      description: "Permanently delete a resource by id."
      destructive: true
    };
  }
}

message GetResourceRequest {
  string id = 1 [(gortexa.ai.v1.ai_field) = { description: "Resource id (uuid)." required: true }];
}
```

規則：
- `expose: false`（預設）→ 不暴露為 AI tool。明確 opt-in。
- `read_only` 與 `destructive` 不可同真（生成器會報錯）。
- description 寫「做什麼 + 何時用」,會流入三供應商 schema。
- tool 名（含完整 RPC 名）≤ 64 字元,避免 Claude desktop 截斷。

## 型別對齊（B-6,設計時強制）
- proto `int64` ↔ PostgreSQL `bigint`；`string` ↔ `text`。
- JSON 序列化中 int64 為 string。
- 三供應商 schema 中 enum 一律 string；避免 oneOf/default/exclusiveMin/Max（Gemini/OpenAI strict 不支援）。
