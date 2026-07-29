mod worker;
use worker::run as execute;

pub fn call() {
    execute();
}
