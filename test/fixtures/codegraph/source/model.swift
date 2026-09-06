protocol Named { var name: String { get } }
struct Person: Named { let name: String }
class Parent { func greet() -> String { return "hello" } }
class Child: Parent { override func greet() -> String { return super.greet() } }
