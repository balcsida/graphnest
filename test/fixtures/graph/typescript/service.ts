import * as base from "./base";

class Parent {}

export class Service extends Parent {
  run(): void {
    base.work();
  }
}
