---
trigger: always_on
---

The equivalent of:

```cpp
if (!foo->VerifyTypeIs(Value::STRING, err)) {
  return false;
}
std::string foo_string = foo->string_value();
```

Is:

```go
fooString, err := resolve.AsValue[*resolve.StringValue](foo)
if err != nil {
  return err
}
```