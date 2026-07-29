package fixture;

interface Worker {
  void run();
}

class Runner implements Worker {
  public void run() {}
}

class Helper {
  static void execute() {}
}
