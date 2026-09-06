export interface Greeter { greet(name: string): string; }
export class Base { greet(name: string): string { return name; } }
export function normalize(name: string): string { return name.trim(); }
export function logged(target: unknown): void {}
@logged
export class Service extends Base implements Greeter {
  readonly label: string = "fixture";
  override greet(name: string): string { return normalize(name); }
}
export function createService(): Service { return new Service(); }
export const answer = 42;
export let current: Service = createService();
export enum State { Ready, Done }
export type Label = string;
export namespace Helpers { export function identity(value: string) { return value; } }
