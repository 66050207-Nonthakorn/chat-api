def handler(event, context):
    if event.get("triggerSource") == "PreSignUp_SignUp":
        event["response"]["autoConfirmUser"] = True
    return event
