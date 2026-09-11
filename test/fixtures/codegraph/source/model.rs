pub trait Render { fn render(&self) -> String; }
pub struct Model { pub value: i32 }
pub union Number { pub integer: i32, pub floating: f32 }
impl Render for Model { fn render(&self) -> String { self.value.to_string() } }
pub mod nested { pub fn identity(value: i32) -> i32 { value } }
