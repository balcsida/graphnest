import { Service, normalize } from './core';
export function run(): string { const service = new Service(); return service.greet(normalize(' fixture ')); }
