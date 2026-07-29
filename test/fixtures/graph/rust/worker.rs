pub trait Worker {
    fn run();
}

pub struct Runner;

impl Worker for Runner {
    fn run() {}
}

pub fn run() {}
