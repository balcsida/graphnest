import { Base as Parent, doWork as work } from "./base";

interface Runnable {
	run(): void;
}

export function helper() {}

export class Service extends Parent implements Runnable {
	run() {
		work();
	}
}
